package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mulgadc/northstar/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigS3InsecureOnlyInEnvironmentMode(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>dns-zones</Name><KeyCount>0</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated></ListBucketResult>`)
	}))
	t.Cleanup(server.Close)

	t.Setenv("ZONE_DIR", "s3://dns-zones")
	t.Setenv("NORTHSTAR_S3_ENDPOINT", server.URL)
	t.Setenv("NORTHSTAR_S3_INSECURE", "1")
	t.Setenv("S3_SYNC_RETRY", "3")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY", "AKIATEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")

	cfg, err := loadConfig("")
	require.NoError(t, err)
	require.True(t, cfg.S3.Insecure)
	require.Equal(t, 3*time.Second, cfg.SyncDuration())

	zones, err := config.ReadZoneFiles(cfg.ZoneSource(), cfg.S3Pointer())
	require.NoError(t, err)
	require.NotNil(t, zones)

	path := filepath.Join(t.TempDir(), "northstar.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[s3]
endpoint = "https://predastore:9443"
region = "us-east-1"
bucket = "dns-zones"
access_key = "AKIATEST"
secret_key = "secret"
insecure = true
`), 0o600))

	cfg, err = loadConfig(path)
	require.NoError(t, err)
	require.False(t, cfg.S3.Insecure)
}

func TestEnvIntOr(t *testing.T) {
	value, err := envIntOr("UNSET_TEST_INT", 60)
	require.NoError(t, err)
	require.Equal(t, 60, value)

	t.Setenv("TEST_INT", "3")
	value, err = envIntOr("TEST_INT", 60)
	require.NoError(t, err)
	require.Equal(t, 3, value)
}

func TestLoadConfigRejectsInvalidSyncInterval(t *testing.T) {
	t.Setenv("S3_SYNC_RETRY", "invalid")

	_, err := loadConfig("")
	require.ErrorContains(t, err, "S3_SYNC_RETRY")
}

func TestEnvBool(t *testing.T) {
	tests := map[string]struct {
		value   string
		want    bool
		wantErr bool
	}{
		"true":    {value: "true", want: true},
		"one":     {value: "1", want: true},
		"false":   {value: "false"},
		"zero":    {value: "0"},
		"invalid": {value: "invalid", wantErr: true},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TEST_BOOL", test.value)
			value, err := envBool("TEST_BOOL")
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, value)
		})
	}
}
