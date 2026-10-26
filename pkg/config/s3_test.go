package config

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeS3 is a minimal path-style S3 endpoint serving ListObjectsV2 and
// GetObject for a fixed set of zone files. Listing pages like real S3: keys in
// order, at most max-keys (default 1000) per page, with continuation tokens.
func fakeS3(t *testing.T, bucket string, objects map[string]string) *S3Config {
	t.Helper()
	return fakeS3Paged(t, bucket, objects, nil)
}

// fakeS3Paged is fakeS3 with a hook that can fail a listing page: failPage is
// called with the page index and returns true to answer that page with a 500.
func fakeS3Paged(t *testing.T, bucket string, objects map[string]string, failPage func(page int) bool) *S3Config {
	t.Helper()
	const lastMod = "2022-05-27T07:32:00.000Z"

	keys := make([]string, 0, len(objects))
	for k := range objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// ListObjectsV2: GET /bucket/?list-type=2
		if q.Get("list-type") == "2" {
			maxKeys := 1000
			if v, err := strconv.Atoi(q.Get("max-keys")); err == nil && v > 0 && v < maxKeys {
				maxKeys = v
			}
			start := 0
			if tok := q.Get("continuation-token"); tok != "" {
				start, _ = strconv.Atoi(tok)
			}
			if failPage != nil && failPage(start/maxKeys) {
				http.Error(w, "injected failure", http.StatusInternalServerError)
				return
			}
			end := min(start+maxKeys, len(keys))

			var b strings.Builder
			b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
			b.WriteString(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
			fmt.Fprintf(&b, "<Name>%s</Name><KeyCount>%d</KeyCount><MaxKeys>%d</MaxKeys>", bucket, end-start, maxKeys)
			for _, key := range keys[start:end] {
				fmt.Fprintf(&b, "<Contents><Key>%s</Key><LastModified>%s</LastModified><Size>%d</Size></Contents>", key, lastMod, len(objects[key]))
			}
			if end < len(keys) {
				fmt.Fprintf(&b, "<IsTruncated>true</IsTruncated><NextContinuationToken>%d</NextContinuationToken>", end)
			} else {
				b.WriteString("<IsTruncated>false</IsTruncated>")
			}
			b.WriteString(`</ListBucketResult>`)
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(b.String()))
			return
		}

		// GetObject: GET /bucket/<key>
		prefix := "/" + bucket + "/"
		key := strings.TrimPrefix(r.URL.Path, prefix)
		body, ok := objects[key]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return &S3Config{
		Endpoint:  srv.URL,
		Region:    "us-east-1",
		Bucket:    bucket,
		AccessKey: "AKIATEST",
		SecretKey: "secret",
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bare host port gets HTTPS",
			in:   "192.168.1.29:8443",
			want: "https://192.168.1.29:8443",
		},
		{
			name: "HTTPS URL unchanged",
			in:   "https://127.0.0.1:8443",
			want: "https://127.0.0.1:8443",
		},
		{
			name: "HTTP URL unchanged",
			in:   "http://127.0.0.1:8443",
			want: "http://127.0.0.1:8443",
		},
		{
			name: "bare hostname gets HTTPS",
			in:   "predastore.internal",
			want: "https://predastore.internal",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, normalizeEndpoint(tc.in))
		})
	}
}

func TestReadZoneS3(t *testing.T) {
	objects := map[string]string{"example.test.toml": zoneBody("example.test", "10.0.0.1")}
	s3cfg := fakeS3(t, "northstar", objects)

	cfg, err := ReadZone("s3://northstar/example.test.toml", time.Now(), s3cfg)
	require.NoError(t, err)
	assert.Equal(t, "example.test", cfg.Domain.Domain)
	require.NotEmpty(t, cfg.Records)
	assert.Equal(t, "10.0.0.1", cfg.Records[0].Address)
}

func TestReadZoneFilesS3(t *testing.T) {
	objects := map[string]string{
		"one.test.toml": zoneBody("one.test", "10.0.0.1"),
		"two.test.toml": zoneBody("two.test", "10.0.0.2"),
		"ignore.txt":    "not a zone",
	}
	s3cfg := fakeS3(t, "northstar", objects)

	cfg, err := ReadZoneFiles("s3://northstar", s3cfg)
	require.NoError(t, err)
	assert.Len(t, cfg.Domain, 2)
	require.NotEmpty(t, apexRecords(cfg, "one.test"))
	require.NotEmpty(t, apexRecords(cfg, "two.test"))
}

func TestMonitorConfigS3(t *testing.T) {
	objects := map[string]string{"sync.test.toml": zoneBody("sync.test", "10.0.0.7")}
	s3cfg := fakeS3(t, "northstar", objects)

	cfg := &Config{Domain: map[string]Domain{}, Records: map[DomainLookup][]Records{}}
	go cfg.MonitorConfig(t.Context(), "s3://northstar", s3cfg, 50*time.Millisecond)

	require.Eventually(t, func() bool {
		return len(apexRecords(cfg, "sync.test")) > 0
	}, 3*time.Second, 50*time.Millisecond)
}

// manyZones returns n zone objects named d0001.test … so they sort in order and
// span more than one 1000-key listing page when n > 1000.
func manyZones(n int) map[string]string {
	objects := make(map[string]string, n)
	for i := 1; i <= n; i++ {
		domain := fmt.Sprintf("d%04d.test", i)
		objects[domain+".toml"] = zoneBody(domain, fmt.Sprintf("10.0.%d.%d", i/250, i%250+1))
	}
	return objects
}

func TestReadZoneFilesS3_LoadsEveryPage(t *testing.T) {
	s3cfg := fakeS3(t, "northstar", manyZones(1100))

	cfg, err := ReadZoneFiles("s3://northstar", s3cfg)
	require.NoError(t, err)
	assert.Len(t, cfg.Domain, 1100)
	require.NotEmpty(t, apexRecords(cfg, "d0001.test"))
	require.NotEmpty(t, apexRecords(cfg, "d1100.test"))
}

// A sync over a bucket larger than one listing page must keep every zone. The
// first-page-only listing purged everything past key 1000 on each cycle.
func TestSyncS3_KeepsZonesBeyondFirstPage(t *testing.T) {
	s3cfg := fakeS3(t, "northstar", manyZones(1100))

	cfg, err := ReadZoneFiles("s3://northstar", s3cfg)
	require.NoError(t, err)
	require.Len(t, cfg.Domain, 1100)

	require.NoError(t, cfg.syncS3(t.Context(), newS3Client(s3cfg), "s3://northstar", s3cfg))
	assert.Len(t, cfg.Domain, 1100)
	require.NotEmpty(t, apexRecords(cfg, "d1100.test"))
}

func TestSyncS3_FailedLaterPagePurgesNothing(t *testing.T) {
	objects := manyZones(1100)
	loaded, err := ReadZoneFiles("s3://northstar", fakeS3(t, "northstar", objects))
	require.NoError(t, err)
	require.Len(t, loaded.Domain, 1100)

	failing := fakeS3Paged(t, "northstar", objects, func(page int) bool { return page == 1 })
	err = loaded.syncS3(t.Context(), newS3Client(failing), "s3://northstar", failing)
	require.Error(t, err)
	assert.Len(t, loaded.Domain, 1100)
	require.NotEmpty(t, apexRecords(loaded, "d1100.test"))
}

func TestSyncS3_PurgesZoneRemovedFromBucket(t *testing.T) {
	objects := manyZones(1100)
	cfg, err := ReadZoneFiles("s3://northstar", fakeS3(t, "northstar", objects))
	require.NoError(t, err)

	delete(objects, "d1050.test.toml")
	smaller := fakeS3(t, "northstar", objects)
	require.NoError(t, cfg.syncS3(t.Context(), newS3Client(smaller), "s3://northstar", smaller))
	assert.Len(t, cfg.Domain, 1099)
	assert.Empty(t, apexRecords(cfg, "d1050.test"))
	require.NotEmpty(t, apexRecords(cfg, "d1100.test"))
}
