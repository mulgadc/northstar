package server_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
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

func TestServerStartNoListenAddrs(t *testing.T) {
	srv, err := server.NewServer(config.ServerConfig{Listen: "", ZoneDir: t.TempDir()})
	require.NoError(t, err)
	require.Error(t, srv.Start(context.Background()))
}
