package server_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/mulgadc/northstar/pkg/config"
	"github.com/mulgadc/northstar/pkg/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	tcpAddr, ok := l.Addr().(*net.TCPAddr)
	require.True(t, ok)
	return tcpAddr.Port
}

func writeZone(t *testing.T, dir, domain, addr string) {
	t.Helper()
	body := fmt.Sprintf(`version = 1.0
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
	require.NoError(t, os.WriteFile(filepath.Join(dir, domain+".toml"), []byte(body), 0o600))
}

func queryA(t *testing.T, addr, name string) *dns.Msg {
	t.Helper()
	c := dns.Client{}
	m := dns.Msg{}
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	r, _, err := c.Exchange(&m, addr)
	require.NoError(t, err)
	return r
}

func TestServerLifecycle(t *testing.T) {
	dir := t.TempDir()
	writeZone(t, dir, "example.test", "10.1.2.3")

	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	srv, err := server.NewServer(config.ServerConfig{
		Listen:       addr,
		ZoneDir:      dir,
		SyncInterval: 1,
	})
	require.NoError(t, err)

	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	// Give the listeners a moment to activate.
	time.Sleep(50 * time.Millisecond)

	r := queryA(t, addr, "example.test")
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	assert.True(t, r.Authoritative)
	require.NotEmpty(t, r.Answer)
	a, ok := r.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "10.1.2.3", a.A.String())

	// Reload picks up a newly added zone.
	writeZone(t, dir, "second.test", "10.9.9.9")
	require.NoError(t, srv.Reload())

	r = queryA(t, addr, "second.test")
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	require.NotEmpty(t, r.Answer)
	a, ok = r.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "10.9.9.9", a.A.String())

	require.NoError(t, srv.Shutdown(context.Background()))
}

// fakeS3 serves ListObjectsV2 + GetObject for one zone file, path-style,
// enough for Server.Reload against an s3:// zone source.
func fakeS3(t *testing.T, bucket, key, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") == "2" {
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>%s</Name><Contents><Key>%s</Key><LastModified>2022-05-27T07:32:00.000Z</LastModified><Size>%d</Size></Contents></ListBucketResult>`, bucket, key, len(body))
			return
		}
		if strings.TrimPrefix(r.URL.Path, "/"+bucket+"/") == key {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write([]byte(body))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestServerReloadFailureKeepsZones(t *testing.T) {
	zone := `version = 1.0
[domain]
domain = "example.test"
modified = 2022-05-27T07:32:00Z
[defaults]
ttl = 60
type = 1
class = 1
[[records]]
domain = ""
address = "10.1.2.3"
`
	s3srv := fakeS3(t, "northstar", "example.test.toml", zone)

	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	srv, err := server.NewServer(config.ServerConfig{
		Listen:       addr,
		ZoneDir:      "",
		SyncInterval: 3600,
		S3: config.S3Config{
			Endpoint:  s3srv.URL,
			Region:    "us-east-1",
			Bucket:    "northstar",
			AccessKey: "AKIATEST",
			SecretKey: "secret",
		},
	})
	require.NoError(t, err)
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	time.Sleep(50 * time.Millisecond)

	r := queryA(t, addr, "example.test")
	require.Equal(t, dns.RcodeSuccess, r.Rcode)

	// Transient S3 outage: Reload must return an error and keep serving the
	// existing zone DB, not swap in an empty one.
	s3srv.Close()
	require.Error(t, srv.Reload())

	r = queryA(t, addr, "example.test")
	assert.Equal(t, dns.RcodeSuccess, r.Rcode)
	assert.True(t, r.Authoritative)
	require.NotEmpty(t, r.Answer)
}

func TestServerStartNoListenAddrs(t *testing.T) {
	srv, err := server.NewServer(config.ServerConfig{Listen: "", ZoneDir: t.TempDir()})
	require.NoError(t, err)
	require.Error(t, srv.Start(context.Background()))
}

func TestServerStartBindError(t *testing.T) {
	// Port out of range → UDP/TCP bind fails and Start returns the error.
	srv, err := server.NewServer(config.ServerConfig{Listen: "127.0.0.1:99999", ZoneDir: t.TempDir()})
	require.NoError(t, err)
	require.Error(t, srv.Start(context.Background()))
}

func TestServerStartDoTMissingCert(t *testing.T) {
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	dot := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	srv, err := server.NewServer(config.ServerConfig{Listen: addr, DotListen: dot, ZoneDir: t.TempDir()})
	require.NoError(t, err)
	require.Error(t, srv.Start(context.Background()))
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
}

func TestServerStartDoHMissingCert(t *testing.T) {
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	doh := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	srv, err := server.NewServer(config.ServerConfig{Listen: addr, DohListen: doh, ZoneDir: t.TempDir()})
	require.NoError(t, err)
	require.Error(t, srv.Start(context.Background()))
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
}
