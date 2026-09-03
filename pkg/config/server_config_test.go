package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTOML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "northstar.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestLoadServerConfigS3(t *testing.T) {
	path := writeTOML(t, `
listen = "0.0.0.0:5300,0.0.0.0:53"
default_domain = "spx3.net"
sync_interval = 15

[s3]
endpoint = "https://127.0.0.1:8443"
bucket = "northstar"
region = "ap-southeast-2"
access_key = "AKIATEST"
secret_key = "secret"

[quotas]
enabled = true
records_per_hosted_zone = 2500

[upstream]
nameservers = ["1.1.1.1:53", "tls://8.8.8.8:853"]
`)

	cfg, err := LoadServerConfig(path)
	require.NoError(t, err)

	assert.Equal(t, []string{"0.0.0.0:5300", "0.0.0.0:53"}, cfg.ListenAddrs())
	assert.Equal(t, "spx3.net", cfg.DefaultDomain)
	assert.Equal(t, 15, cfg.SyncInterval)
	assert.Equal(t, "s3://northstar", cfg.ZoneSource())
	assert.Equal(t, []string{"1.1.1.1:53", "tls://8.8.8.8:853"}, cfg.Upstream.Nameservers)
	assert.Equal(t, Quotas{Enabled: true, RecordsPerHostedZone: 2500}, cfg.Quotas)

	s3 := cfg.S3Pointer()
	require.NotNil(t, s3)
	assert.Equal(t, "northstar", s3.Bucket)
	assert.Equal(t, "AKIATEST", s3.AccessKey)
}

func TestLoadServerConfigDefaults(t *testing.T) {
	path := writeTOML(t, `zone_dir = "/etc/spinifex/zones"`)

	cfg, err := LoadServerConfig(path)
	require.NoError(t, err)

	assert.Equal(t, defaultListen, cfg.Listen)
	assert.Equal(t, defaultSyncInterval, cfg.SyncInterval)
	assert.Equal(t, defaultInternalDomain, cfg.InternalDomain)
	assert.Equal(t, "/etc/spinifex/zones", cfg.ZoneSource())
	assert.Nil(t, cfg.S3Pointer())
	assert.Equal(t, Quotas{}, cfg.Quotas, "an absent [quotas] block must be disabled")
}

func TestLoadServerConfigInternalAndNats(t *testing.T) {
	path := writeTOML(t, `zone_dir = "/z"
internal_domain = "internal.example"
nats_url = "nats://127.0.0.1:4222"`)

	cfg, err := LoadServerConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "internal.example", cfg.InternalDomain)
	assert.Equal(t, "nats://127.0.0.1:4222", cfg.NatsURL)
}

func TestLoadServerConfigMissingFile(t *testing.T) {
	_, err := LoadServerConfig(filepath.Join(t.TempDir(), "nope.toml"))
	require.Error(t, err)
}

func TestLoadServerConfigValidation(t *testing.T) {
	cases := map[string]string{
		"no zone source":       `listen = "0.0.0.0:53"`,
		"s3 missing creds":     "[s3]\nbucket = \"northstar\"\n",
		"tls cert without key": "zone_dir = \"/z\"\ntls_cert = \"/c.pem\"\n",
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadServerConfig(writeTOML(t, body))
			require.Error(t, err)
		})
	}
}

func TestAllowRecursionDefaultsClosed(t *testing.T) {
	cfg, err := LoadServerConfig(writeTOML(t, `
zone_dir = "/tmp/zones"
`))
	require.NoError(t, err)
	assert.False(t, cfg.Upstream.AllowRecursion, "recursion must default closed on a public-facing resolver")
	assert.Empty(t, cfg.Upstream.AllowRecursionFrom)
}

func TestParseAllowRecursionFrom(t *testing.T) {
	cfg, err := LoadServerConfig(writeTOML(t, `
zone_dir = "/tmp/zones"

[upstream]
allow_recursion_from = ["10.2.0.2", "72.52.77.224/27", "2001:db8::/32"]
`))
	require.NoError(t, err)

	prefixes, err := cfg.Upstream.ParseAllowRecursionFrom()
	require.NoError(t, err)
	require.Len(t, prefixes, 3)
	// A bare address becomes a single-host prefix, so node IPs need no /32.
	assert.Equal(t, "10.2.0.2/32", prefixes[0].String())
	assert.Equal(t, "72.52.77.224/27", prefixes[1].String())
	assert.Equal(t, "2001:db8::/32", prefixes[2].String())
}

func TestAllowRecursionFromRejectsGarbageAtLoad(t *testing.T) {
	_, err := LoadServerConfig(writeTOML(t, `
zone_dir = "/tmp/zones"

[upstream]
allow_recursion_from = ["not-an-address"]
`))
	require.Error(t, err, "a malformed entry must fail startup, not every query")
	assert.Contains(t, err.Error(), "allow_recursion_from")
}
