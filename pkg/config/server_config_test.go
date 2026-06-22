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
insecure = true

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

	s3 := cfg.S3Pointer()
	require.NotNil(t, s3)
	assert.Equal(t, "northstar", s3.Bucket)
	assert.Equal(t, "AKIATEST", s3.AccessKey)
	assert.True(t, s3.Insecure)
}

func TestLoadServerConfigDefaults(t *testing.T) {
	path := writeTOML(t, `zone_dir = "/etc/spinifex/zones"`)

	cfg, err := LoadServerConfig(path)
	require.NoError(t, err)

	assert.Equal(t, defaultListen, cfg.Listen)
	assert.Equal(t, defaultSyncInterval, cfg.SyncInterval)
	assert.Equal(t, "/etc/spinifex/zones", cfg.ZoneSource())
	assert.Nil(t, cfg.S3Pointer())
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
