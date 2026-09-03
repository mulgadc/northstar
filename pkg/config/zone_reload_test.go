package config

import (
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormaliseETag(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"quoted", `"abc123"`, "abc123"},
		{"bare", "abc123", "abc123"},
		{"weak validator", `W/"abc123"`, "abc123"},
		{"surrounding space", `  "abc123" `, "abc123"},
		{"empty", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, normaliseETag(tc.in))
		})
	}
}

// A listing whose ETag matches the loaded zone is not a change, whatever the
// timestamp says. This is the case that produced 240 reloads an hour on a
// cluster whose S3 backend answered ListObjectsV2 with a fresh time.Now().
func TestZoneChangedPrefersETagOverTimestamp(t *testing.T) {
	loadedAt := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	loaded := Domain{Domain: "spx3.net", Modified: loadedAt, ETag: "abc123"}

	tests := []struct {
		name string
		item s3types.Object
		want bool
	}{
		{
			name: "same etag, timestamp moved",
			item: s3types.Object{ETag: aws.String(`"abc123"`), LastModified: aws.Time(time.Now())},
			want: false,
		},
		{
			name: "different etag, same timestamp",
			item: s3types.Object{ETag: aws.String(`"def456"`), LastModified: aws.Time(loadedAt)},
			want: true,
		},
		{
			name: "same etag unquoted",
			item: s3types.Object{ETag: aws.String("abc123"), LastModified: aws.Time(time.Now())},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, zoneChanged(tc.item, loaded))
		})
	}
}

// Without an ETag on either side the timestamp is all there is, and it must be
// compared as an instant: struct equality also compares the monotonic reading
// and the location pointer, so two values for the same moment can differ.
func TestZoneChangedFallsBackToTimestamp(t *testing.T) {
	loadedAt := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	loaded := Domain{Domain: "spx3.net", Modified: loadedAt}

	sameInstantOtherLocation := loadedAt.In(time.FixedZone("AEST", 10*60*60))
	require.True(t, sameInstantOtherLocation.Equal(loadedAt), "must be one instant")
	require.NotEqual(t, loadedAt.Location(), sameInstantOtherLocation.Location(),
		"test needs two differing structs for that instant")

	tests := []struct {
		name   string
		item   s3types.Object
		loaded Domain
		want   bool
	}{
		{
			name: "same instant in another location is not a change",
			item: s3types.Object{LastModified: aws.Time(sameInstantOtherLocation)},
			want: false,
		},
		{
			name: "later timestamp is a change",
			item: s3types.Object{LastModified: aws.Time(loadedAt.Add(time.Second))},
			want: true,
		},
		{
			name: "listing has an etag but the zone was loaded without one",
			item: s3types.Object{ETag: aws.String(`"abc123"`), LastModified: aws.Time(loadedAt)},
			want: false,
		},
		{
			name: "no timestamp at all is not a change",
			item: s3types.Object{},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, zoneChanged(tc.item, loaded))
		})
	}
}

// The reload must never expose a window in which the server is not
// authoritative for a zone it owns. DeleteZone followed by AddZone released the
// lock between the two calls, so a query landing in the gap took the recursion
// branch for a name we own.
func TestReloadKeepsTheZoneAuthoritativeThroughout(t *testing.T) {
	db := newConfigDB()
	seed := BaseZoneSeed{
		Domain:      "spx3.net",
		Nameservers: []NameserverSeed{{Host: "ns1", IP: "10.0.0.1"}},
	}
	loadZone(db, seed)

	var wg sync.WaitGroup
	done := make(chan struct{})

	wg.Go(func() {
		for range 500 {
			cfg := NewZoneConfig(seed)
			ApplyDefaults(&cfg, time.Now())
			db.ReplaceZone(cfg)
		}
		close(done)
	})

	wg.Go(func() {
		for {
			select {
			case <-done:
				return
			default:
			}

			zone, isAuth := db.FindZone("ns1.spx3.net.")
			if !assert.True(t, isAuth, "zone vanished mid-reload") {
				return
			}
			assert.Equal(t, "spx3.net", zone)
		}
	})

	wg.Wait()

	_, isAuth := db.FindZone("ns1.spx3.net.")
	assert.True(t, isAuth, "zone must survive the reloads")
}
