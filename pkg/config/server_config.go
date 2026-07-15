package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// ServerConfig is the top-level northstar.toml schema: the single source of
// truth for an embedded or standalone Northstar server. It is distinct from
// Config, which is the in-memory zone database.
type ServerConfig struct {
	// Listen is a comma-separated list of UDP+TCP bind addresses, e.g.
	// "0.0.0.0:5300,0.0.0.0:53".
	Listen        string `toml:"listen"`
	DotListen     string `toml:"dot_listen"`
	DohListen     string `toml:"doh_listen"`
	TLSCert       string `toml:"tls_cert"`
	TLSKey        string `toml:"tls_key"`
	DefaultDomain string `toml:"default_domain"`
	// InternalDomain is the AWS-parity private DNS zone (default compute.internal)
	// under which instance ip-<addr> records are served.
	InternalDomain string `toml:"internal_domain"`
	// NatsURL, when set, lets the control plane push live zone-reload events so a
	// change is served immediately rather than after the SyncInterval poll. The
	// northstar library itself does not use it; the spinifex service wrapper does.
	NatsURL      string         `toml:"nats_url"`
	SyncInterval int            `toml:"sync_interval"`
	ZoneDir      string         `toml:"zone_dir"`
	S3           S3Config       `toml:"s3"`
	Upstream     UpstreamConfig `toml:"upstream"`
	Quotas       Quotas         `toml:"quotas"`
}

// Quotas holds per-deployment DNS service-quota overrides, mirroring the
// [quota] block in awsgw.toml. The zero value disables quota enforcement.
type Quotas struct {
	Enabled              bool `toml:"enabled"`
	RecordsPerHostedZone int  `toml:"records_per_hosted_zone"`
}

// UpstreamConfig lists forwarders for non-authoritative queries. An empty list
// means non-authoritative queries are refused (air-gap safe).
type UpstreamConfig struct {
	Nameservers []string `toml:"nameservers"`
}

const (
	defaultListen         = "0.0.0.0:5300"
	defaultSyncInterval   = 30
	defaultInternalDomain = "compute.internal"
)

// LoadServerConfig reads and validates a northstar.toml file, applying defaults.
func LoadServerConfig(path string) (ServerConfig, error) {
	var cfg ServerConfig

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}

	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}

	cfg.applyDefaults()

	if err := cfg.validate(); err != nil {
		return cfg, fmt.Errorf("invalid %s: %w", path, err)
	}

	return cfg, nil
}

func (c *ServerConfig) applyDefaults() {
	if c.Listen == "" {
		c.Listen = defaultListen
	}
	if c.SyncInterval <= 0 {
		c.SyncInterval = defaultSyncInterval
	}
	if c.InternalDomain == "" {
		c.InternalDomain = defaultInternalDomain
	}
}

func (c *ServerConfig) validate() error {
	if c.S3.Bucket == "" && c.ZoneDir == "" {
		return fmt.Errorf("one of [s3].bucket or zone_dir is required")
	}
	if c.S3.Bucket != "" && (c.S3.AccessKey == "" || c.S3.SecretKey == "") {
		return fmt.Errorf("[s3].bucket set but access_key/secret_key missing")
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return fmt.Errorf("tls_cert and tls_key must be set together")
	}
	return nil
}

// ListenAddrs returns the parsed, trimmed list of bind addresses.
func (c *ServerConfig) ListenAddrs() []string {
	var addrs []string
	for a := range strings.SplitSeq(c.Listen, ",") {
		if a = strings.TrimSpace(a); a != "" {
			addrs = append(addrs, a)
		}
	}
	return addrs
}

// ZoneSource returns the zone source: an s3://bucket URL when S3 is configured,
// otherwise the filesystem ZoneDir.
func (c *ServerConfig) ZoneSource() string {
	if c.S3.Bucket != "" {
		return "s3://" + c.S3.Bucket
	}
	return c.ZoneDir
}

// S3Pointer returns a pointer to the S3 config when a bucket is set, else nil
// (filesystem mode).
func (c *ServerConfig) S3Pointer() *S3Config {
	if c.S3.Bucket == "" {
		return nil
	}
	s3 := c.S3
	return &s3
}

// SyncDuration returns the S3 re-scan interval.
func (c *ServerConfig) SyncDuration() time.Duration {
	return time.Duration(c.SyncInterval) * time.Second
}
