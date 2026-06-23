package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newConfigDB() *Config {
	return &Config{
		Records: make(map[DomainLookup][]Records),
		Domain:  make(map[string]Domain),
	}
}

// applyDefaultsToZone mirrors the FQDN-label form a parsed zone holds in the
// live DB, so ReplaceZone keys line up with what queries look up.
func loadZone(db *Config, seed BaseZoneSeed) {
	cfg := NewZoneConfig(seed)
	ApplyDefaults(&cfg, time.Now())
	db.ReplaceZone(cfg)
}

func TestReplaceZoneInsertsAndIndexes(t *testing.T) {
	db := newConfigDB()
	loadZone(db, BaseZoneSeed{
		Domain:      "spx3.net",
		Nameservers: []NameserverSeed{{Host: "ns1", IP: "10.0.0.1"}},
	})

	zone, ok := db.Domain["spx3.net"]
	require.True(t, ok)
	require.NotEmpty(t, zone.RecordRef)
	// Every indexed RecordRef must resolve to a populated RRset.
	for _, ref := range zone.RecordRef {
		assert.NotEmpty(t, db.Records[ref], "ref %+v missing records", ref)
	}
}

func TestReplaceZoneSwapLeavesOtherZonesIntact(t *testing.T) {
	db := newConfigDB()
	loadZone(db, BaseZoneSeed{Domain: "spx3.net", Nameservers: []NameserverSeed{{Host: "ns1", IP: "10.0.0.1"}}})
	loadZone(db, BaseZoneSeed{Domain: "other.net", Nameservers: []NameserverSeed{{Host: "ns1", IP: "10.9.9.9"}}})

	otherBefore := db.Domain["other.net"].RecordRef

	// Re-load spx3.net with an extra host record.
	cfg := NewZoneConfig(BaseZoneSeed{Domain: "spx3.net", Nameservers: []NameserverSeed{{Host: "ns1", IP: "10.0.0.1"}}})
	cfg.UpsertRecord("web.", TypeA, ClassIN, "10.0.0.40", 60)
	ApplyDefaults(&cfg, time.Now())
	db.ReplaceZone(cfg)

	// other.net is untouched.
	assert.Equal(t, otherBefore, db.Domain["other.net"].RecordRef)

	// The new web record is queryable and no stale keys linger for spx3.net.
	web := DomainLookup{Domain: "web.spx3.net.", Type: TypeA, Class: ClassIN}
	require.NotEmpty(t, db.Records[web])
	assert.Equal(t, "10.0.0.40", db.Records[web][0].Address)
}

func TestReplaceZoneRemovesStaleKeys(t *testing.T) {
	db := newConfigDB()
	first := NewZoneConfig(BaseZoneSeed{Domain: "spx3.net", Nameservers: []NameserverSeed{{Host: "ns1", IP: "10.0.0.1"}}})
	first.UpsertRecord("old.", TypeA, ClassIN, "10.0.0.99", 60)
	ApplyDefaults(&first, time.Now())
	db.ReplaceZone(first)

	stale := DomainLookup{Domain: "old.spx3.net.", Type: TypeA, Class: ClassIN}
	require.NotEmpty(t, db.Records[stale])

	// Reload without the "old" record: its key must be evicted.
	second := NewZoneConfig(BaseZoneSeed{Domain: "spx3.net", Nameservers: []NameserverSeed{{Host: "ns1", IP: "10.0.0.1"}}})
	ApplyDefaults(&second, time.Now())
	db.ReplaceZone(second)

	assert.Empty(t, db.Records[stale], "stale RRset must be evicted on zone swap")
}
