package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func zoneBody(domain, addr string) string {
	return fmt.Sprintf(`version = 1.0
[domain]
domain = "%s"
modified = 2022-05-27T07:32:00Z
[defaults]
ttl = 60
type = 1
class = 1
[[records]]
domain = ""
address = "%s"
`, domain, addr)
}

func writeZoneFile(t *testing.T, dir, domain, addr string) string {
	t.Helper()
	path := filepath.Join(dir, domain+".toml")
	require.NoError(t, os.WriteFile(path, []byte(zoneBody(domain, addr)), 0o600))
	return path
}

func apexRecords(c *Config, domain string) []Records {
	lookup := DomainLookup{Domain: domain + ".", Type: 1, Class: 1}
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	return c.Records[lookup]
}

func TestReadZoneFilesystem(t *testing.T) {
	dir := t.TempDir()
	path := writeZoneFile(t, dir, "example.test", "10.0.0.1")

	cfg, err := ReadZone(path, time.Now(), nil)
	require.NoError(t, err)
	assert.Equal(t, "example.test", cfg.Domain.Domain)
	require.NotEmpty(t, cfg.Records)
	assert.Equal(t, "10.0.0.1", cfg.Records[0].Address)
}

func TestReadZoneErrors(t *testing.T) {
	// Missing file.
	_, err := ReadZone(filepath.Join(t.TempDir(), "nope.toml"), time.Now(), nil)
	require.Error(t, err)

	// Malformed TOML.
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.toml")
	require.NoError(t, os.WriteFile(bad, []byte("not = valid = toml"), 0o600))
	_, err = ReadZone(bad, time.Now(), nil)
	require.Error(t, err)

	// s3:// without config.
	_, err = ReadZone("s3://bucket/zone.toml", time.Now(), nil)
	require.Error(t, err)
}

func TestReadZoneFilesDir(t *testing.T) {
	dir := t.TempDir()
	writeZoneFile(t, dir, "one.test", "10.0.0.1")
	writeZoneFile(t, dir, "two.test", "10.0.0.2")
	// A non-zone file is ignored.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("ignore"), 0o600))

	cfg := ReadZoneFiles(dir, nil)
	assert.Len(t, cfg.Domain, 2)
	require.NotEmpty(t, apexRecords(cfg, "one.test"))
	require.NotEmpty(t, apexRecords(cfg, "two.test"))
}

func TestReadZoneFilesS3NoConfig(t *testing.T) {
	cfg := ReadZoneFiles("s3://bucket", nil)
	assert.Empty(t, cfg.Domain)
}

func TestMonitorConfigFilesystem(t *testing.T) {
	dir := t.TempDir()
	writeZoneFile(t, dir, "initial.test", "10.0.0.1")

	cfg := ReadZoneFiles(dir, nil)
	require.NotEmpty(t, apexRecords(cfg, "initial.test"))

	go cfg.MonitorConfig(t.Context(), dir, nil, time.Second)
	time.Sleep(100 * time.Millisecond) // let the watcher attach

	// Create a new zone file → picked up via fsnotify Create/Write.
	writeZoneFile(t, dir, "added.test", "10.0.0.2")
	require.Eventually(t, func() bool {
		return len(apexRecords(cfg, "added.test")) > 0
	}, 3*time.Second, 50*time.Millisecond)

	// Remove the file → zone is deleted.
	require.NoError(t, os.Remove(filepath.Join(dir, "added.test.toml")))
	require.Eventually(t, func() bool {
		return len(apexRecords(cfg, "added.test")) == 0
	}, 3*time.Second, 50*time.Millisecond)
}

func TestMonitorConfigS3RequiresConfig(t *testing.T) {
	cfg := &Config{Domain: map[string]Domain{}, Records: map[DomainLookup][]Records{}}
	// s3:// with nil config returns immediately without blocking.
	done := make(chan struct{})
	go func() { cfg.MonitorConfig(t.Context(), "s3://bucket", nil, time.Second); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("MonitorConfig did not return for s3:// with nil config")
	}
}
