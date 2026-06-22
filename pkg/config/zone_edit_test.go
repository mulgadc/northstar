package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderZoneRoundTrips(t *testing.T) {
	seed := BaseZoneSeed{
		Domain:      "spx3.net",
		Nameservers: []NameserverSeed{{Host: "ns1", IP: "10.0.0.1"}},
		TXT:         []string{"v=spinifex1"},
		TTL:         300,
	}
	cfg := NewZoneConfig(seed)
	cfg.UpsertRecord("ec2-1-2-3-4.ap-southeast-2.compute.", TypeA, ClassIN, "1.2.3.4", 60)

	body, err := RenderZone(cfg)
	require.NoError(t, err)

	// Re-parse through the normal reader (which applies defaults → FQDNs).
	parsed, err := ReadZoneFromBytes(t, body, "spx3.net")
	require.NoError(t, err)
	assert.Equal(t, "spx3.net", parsed.Domain.Domain)

	var foundA bool
	for _, rec := range parsed.Records {
		if rec.Type == TypeA && rec.Address == "1.2.3.4" {
			assert.Equal(t, "ec2-1-2-3-4.ap-southeast-2.compute.spx3.net.", rec.Domain)
			assert.Equal(t, uint32(60), rec.TTL)
			foundA = true
		}
	}
	assert.True(t, foundA, "upserted A record present after round-trip")
}

func TestRenderZoneNoDomain(t *testing.T) {
	_, err := RenderZone(ConfigArr{})
	require.Error(t, err)
}

func TestUpsertRecordReplacesRRset(t *testing.T) {
	cfg := ConfigArr{Domain: Domain{Domain: "spx3.net"}}

	assert.True(t, cfg.UpsertRecord("api.", TypeA, ClassIN, "1.1.1.1", 60))
	require.Len(t, cfg.Records, 1)

	// Same name+type with a new value replaces (not appends).
	assert.True(t, cfg.UpsertRecord("api.", TypeA, ClassIN, "2.2.2.2", 60))
	require.Len(t, cfg.Records, 1)
	assert.Equal(t, "2.2.2.2", cfg.Records[0].Address)

	// Identical upsert reports no change.
	assert.False(t, cfg.UpsertRecord("api.", TypeA, ClassIN, "2.2.2.2", 60))

	// A different name appends.
	assert.True(t, cfg.UpsertRecord("db.", TypeA, ClassIN, "3.3.3.3", 60))
	require.Len(t, cfg.Records, 2)
}

func TestRemoveRecord(t *testing.T) {
	cfg := ConfigArr{Domain: Domain{Domain: "spx3.net"}}
	cfg.UpsertRecord("api.", TypeA, ClassIN, "1.1.1.1", 60)
	cfg.UpsertRecord("db.", TypeA, ClassIN, "2.2.2.2", 60)

	assert.False(t, cfg.RemoveRecord("api.", TypeA, "9.9.9.9"), "value mismatch removes nothing")
	assert.True(t, cfg.RemoveRecord("api.", TypeA, "1.1.1.1"))
	require.Len(t, cfg.Records, 1)
	assert.Equal(t, "db.", cfg.Records[0].Domain)

	assert.True(t, cfg.RemoveRecord("db.", TypeA, ""), "empty value matches any address")
	assert.Empty(t, cfg.Records)
}

func TestReadZoneRawRelativeLabels(t *testing.T) {
	s3cfg, _ := mutableS3(t, "northstar")
	seed := BaseZoneSeed{
		Domain:      "spx3.net",
		Nameservers: []NameserverSeed{{Host: "ns1", IP: "10.0.0.1"}},
	}
	created, err := EnsureBaseZone(s3cfg, seed)
	require.NoError(t, err)
	require.True(t, created)

	cfg, exists, err := ReadZoneRaw(s3cfg, "spx3.net")
	require.NoError(t, err)
	require.True(t, exists)

	// Raw read must keep labels relative (no zone suffix, no double-append).
	for _, rec := range cfg.Records {
		assert.NotContains(t, rec.Domain, "spx3.net", "raw label stays relative: %q", rec.Domain)
	}

	// Missing zone reports exists=false.
	_, exists, err = ReadZoneRaw(s3cfg, "absent.net")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestNewZoneConfigShape(t *testing.T) {
	cfg := NewZoneConfig(BaseZoneSeed{
		Domain:      "compute.internal",
		Nameservers: []NameserverSeed{{Host: "ns1", IP: "10.0.0.1"}},
	})
	assert.Equal(t, "compute.internal", cfg.Domain.Domain)
	assert.Equal(t, "ns1.compute.internal.", cfg.Domain.SOA)
	assert.False(t, cfg.Domain.Modified.IsZero())
	assert.WithinDuration(t, time.Now(), cfg.Domain.Modified, time.Minute)
}
