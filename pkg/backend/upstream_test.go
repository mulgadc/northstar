package backend_test

import (
	"net"
	"testing"

	"github.com/miekg/dns"
	"github.com/mulgadc/northstar/pkg/backend"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startFakeUpstream runs an in-process resolver answering a single name, used to
// exercise Upstream.Exchange and Upstream.Resolve without external network.
func startFakeUpstream(t *testing.T, name, ip string) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)

	handler := dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		if len(r.Question) > 0 && r.Question[0].Name == dns.Fqdn(name) {
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: dns.Fqdn(name), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   net.ParseIP(ip),
			})
		} else {
			m.SetRcode(r, dns.RcodeNameError)
		}
		_ = w.WriteMsg(m)
	})

	ds := &dns.Server{PacketConn: pc, Handler: handler}
	go func() { _ = ds.ActivateAndServe() }()
	t.Cleanup(func() { _ = ds.Shutdown() })
	return pc.LocalAddr().String()
}

func TestParseUpstreamServers(t *testing.T) {
	servers := backend.ParseUpstreamServers([]string{
		"8.8.8.8:53",
		" tls://1.1.1.1:853 ",
		"",
	})
	require.Len(t, servers, 2)
	assert.Equal(t, "8.8.8.8:53", servers[0].Address)
	assert.False(t, servers[0].UseTLS)
	assert.Equal(t, "1.1.1.1:853", servers[1].Address)
	assert.True(t, servers[1].UseTLS)
}

func TestUpstreamHasServers(t *testing.T) {
	assert.False(t, backend.NewUpstream(nil).HasServers())
	assert.True(t, backend.NewUpstream(backend.ParseUpstreamServers([]string{"8.8.8.8:53"})).HasServers())
}

func TestUpstreamExchange(t *testing.T) {
	addr := startFakeUpstream(t, "example.org", "203.0.113.5")
	u := backend.NewUpstream(backend.ParseUpstreamServers([]string{addr}))

	m := new(dns.Msg)
	m.SetQuestion("example.org.", dns.TypeA)
	resp, err := u.Exchange(m)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.Answer)
	a, ok := resp.Answer[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "203.0.113.5", a.A.String())
}

func TestUpstreamExchangeNoServers(t *testing.T) {
	m := new(dns.Msg)
	m.SetQuestion("example.org.", dns.TypeA)
	_, err := backend.NewUpstream(nil).Exchange(m)
	require.Error(t, err)
}

func TestUpstreamExchangeFailover(t *testing.T) {
	good := startFakeUpstream(t, "example.org", "203.0.113.9")
	// First server points at an unused port to force failover to the second.
	u := backend.NewUpstream(backend.ParseUpstreamServers([]string{"127.0.0.1:1", good}))

	m := new(dns.Msg)
	m.SetQuestion("example.org.", dns.TypeA)
	resp, err := u.Exchange(m)
	require.NoError(t, err)
	require.NotEmpty(t, resp.Answer)
}

func TestUpstreamResolve(t *testing.T) {
	addr := startFakeUpstream(t, "example.org", "203.0.113.7")
	u := backend.NewUpstream(backend.ParseUpstreamServers([]string{addr}))

	rrs, err := u.Resolve("example.org", dns.TypeA)
	require.NoError(t, err)
	require.NotEmpty(t, rrs)
	a, ok := rrs[0].(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "203.0.113.7", a.A.String())
}

func TestUpstreamResolveAllFail(t *testing.T) {
	u := backend.NewUpstream(backend.ParseUpstreamServers([]string{"127.0.0.1:1"}))
	_, err := u.Resolve("example.org", dns.TypeA)
	require.Error(t, err)
}
