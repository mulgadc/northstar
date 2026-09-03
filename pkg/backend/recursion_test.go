package backend_test

import (
	"net"
	"net/netip"
	"testing"

	"github.com/miekg/dns"
	"github.com/mulgadc/northstar/pkg/backend"
	"github.com/mulgadc/northstar/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingUpstream is a dns.Handler standing in for the forwarders, so a test
// can assert the query was never sent rather than only that the client got
// REFUSED. Answering from cache while still refusing would pass the weaker
// check and still be a leak.
type recordingUpstream struct {
	addr    string
	queries chan string
}

func newRecordingUpstream(t *testing.T) *recordingUpstream {
	t.Helper()

	up := &recordingUpstream{queries: make(chan string, 16)}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	up.addr = pc.LocalAddr().String()

	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		if len(r.Question) > 0 {
			up.queries <- r.Question[0].Name
		}
		m := new(dns.Msg)
		m.SetReply(r)
		m.RecursionAvailable = true
		m.Answer = append(m.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.IPv4(203, 0, 113, 1),
		})
		_ = w.WriteMsg(m)
	})}
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })

	return up
}

func (u *recordingUpstream) forwarded() bool {
	select {
	case <-u.queries:
		return true
	default:
		return false
	}
}

// aclResponseWriter reports a caller-chosen source address, which is the whole
// input to the policy.
type aclResponseWriter struct {
	remote net.Addr
	msg    *dns.Msg
}

var _ dns.ResponseWriter = (*aclResponseWriter)(nil)

func (w *aclResponseWriter) WriteMsg(m *dns.Msg) error { w.msg = m; return nil }
func (w *aclResponseWriter) Write(b []byte) (int, error) {
	m := new(dns.Msg)
	if err := m.Unpack(b); err != nil {
		return 0, err
	}
	w.msg = m
	return len(b), nil
}
func (w *aclResponseWriter) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 53}
}
func (w *aclResponseWriter) RemoteAddr() net.Addr { return w.remote }
func (w *aclResponseWriter) Close() error         { return nil }
func (w *aclResponseWriter) TsigStatus() error    { return nil }
func (w *aclResponseWriter) TsigTimersOnly(bool)  {}
func (w *aclResponseWriter) Hijack()              {}

func udpFrom(t *testing.T, addr string) net.Addr {
	t.Helper()
	ip := net.ParseIP(addr)
	require.NotNil(t, ip, "bad test address %q", addr)
	return &net.UDPAddr{IP: ip, Port: 33333}
}

// aclHandler builds a handler authoritative for acl.test with a recording
// upstream and the given policy.
func aclHandler(t *testing.T, policy *backend.RecursionPolicy) (*backend.Handler, *recordingUpstream) {
	t.Helper()

	up := newRecordingUpstream(t)
	zoneDB := &config.Config{
		Records: make(map[config.DomainLookup][]config.Records),
		Domain:  make(map[string]config.Domain),
	}
	zoneDB.Domain["acl.test"] = config.Domain{}
	zoneDB.Records[config.DomainLookup{Domain: "host.acl.test.", Type: dns.TypeA, Class: dns.ClassINET}] = []config.Records{
		{Type: dns.TypeA, Class: dns.ClassINET, TTL: 60, Address: "10.1.2.3"},
	}

	upstream := backend.NewUpstream(backend.ParseUpstreamServers([]string{up.addr}))
	return backend.NewHandler(zoneDB, upstream, policy), up
}

func query(t *testing.T, h *backend.Handler, name string, src net.Addr) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	w := &aclResponseWriter{remote: src}
	h.ServeDNS(w, m)
	require.NotNil(t, w.msg)
	return w.msg
}

func trustedPolicy(t *testing.T, cidrs ...string) *backend.RecursionPolicy {
	t.Helper()
	prefixes := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		require.NoError(t, err)
		prefixes = append(prefixes, p)
	}
	return backend.NewRecursionPolicy(false, prefixes)
}

func TestRecursionAllowedForTrustedSource(t *testing.T) {
	h, up := aclHandler(t, trustedPolicy(t, "72.52.77.228/32"))

	resp := query(t, h, "www.example.com", udpFrom(t, "72.52.77.228"))

	assert.Equal(t, dns.RcodeSuccess, resp.Rcode)
	assert.True(t, up.forwarded(), "trusted client should have been forwarded upstream")
}

func TestRecursionRefusedForUntrustedSource(t *testing.T) {
	h, up := aclHandler(t, trustedPolicy(t, "72.52.77.228/32"))

	resp := query(t, h, "www.example.com", udpFrom(t, "198.51.100.7"))

	assert.Equal(t, dns.RcodeRefused, resp.Rcode)
	assert.Empty(t, resp.Answer)
	// ra must be clear: advertising recursion we refuse invites the queries.
	assert.False(t, resp.RecursionAvailable, "ra must not be set for a refused client")
	// The query must not reach the forwarder at all.
	assert.False(t, up.forwarded(), "untrusted client's query must not be forwarded")
}

// The regression that would make this fix worse than the bug: public
// authoritative service must survive the ACL untouched.
func TestAuthoritativeStillServedToUntrustedSource(t *testing.T) {
	h, _ := aclHandler(t, trustedPolicy(t, "72.52.77.228/32"))

	resp := query(t, h, "host.acl.test", udpFrom(t, "198.51.100.7"))

	assert.Equal(t, dns.RcodeSuccess, resp.Rcode)
	assert.True(t, resp.Authoritative)
	require.Len(t, resp.Answer, 1)
	assert.Equal(t, "10.1.2.3", resp.Answer[0].(*dns.A).A.String())
}

func TestNXDOMAINStillServedToUntrustedSource(t *testing.T) {
	h, up := aclHandler(t, trustedPolicy(t, "72.52.77.228/32"))

	resp := query(t, h, "missing.acl.test", udpFrom(t, "198.51.100.7"))

	assert.Equal(t, dns.RcodeNameError, resp.Rcode)
	assert.True(t, resp.Authoritative)
	assert.False(t, up.forwarded(), "our own zone must never reach the forwarder")
}

// A node resolves through its own advertised address, so the query arrives from
// the node's public IP over lo, not from 127.0.0.1. A policy written for
// loopback alone would break every node on the cluster.
func TestRecursionAllowedForNodeOwnPublicAddress(t *testing.T) {
	h, up := aclHandler(t, trustedPolicy(t, "72.52.77.228/32", "10.2.0.2/32"))

	resp := query(t, h, "www.example.com", udpFrom(t, "72.52.77.228"))

	assert.Equal(t, dns.RcodeSuccess, resp.Rcode)
	assert.True(t, up.forwarded())
}

func TestLoopbackAlwaysAllowedRecursion(t *testing.T) {
	h, up := aclHandler(t, trustedPolicy(t))

	resp := query(t, h, "www.example.com", udpFrom(t, "127.0.0.1"))

	assert.Equal(t, dns.RcodeSuccess, resp.Rcode)
	assert.True(t, up.forwarded(), "a resolver must answer its own host")
}

func TestEmptyPolicyRefusesEveryoneElse(t *testing.T) {
	h, up := aclHandler(t, trustedPolicy(t))

	resp := query(t, h, "www.example.com", udpFrom(t, "198.51.100.7"))

	assert.Equal(t, dns.RcodeRefused, resp.Rcode)
	assert.False(t, up.forwarded())
}

func TestAllowRecursionOpensToEveryone(t *testing.T) {
	h, up := aclHandler(t, backend.NewRecursionPolicy(true, nil))

	resp := query(t, h, "www.example.com", udpFrom(t, "198.51.100.7"))

	assert.Equal(t, dns.RcodeSuccess, resp.Rcode)
	assert.True(t, up.forwarded())
}

// A nil policy is the zero-value Handler{} literal used by existing tests, and
// must be authoritative-only rather than open.
func TestNilPolicyRefusesRecursion(t *testing.T) {
	h, up := aclHandler(t, nil)

	resp := query(t, h, "www.example.com", udpFrom(t, "198.51.100.7"))

	assert.Equal(t, dns.RcodeRefused, resp.Rcode)
	assert.False(t, up.forwarded())
}

// An unresolvable source must be refused, not trusted. This is the DoH
// RemoteAddr fallback: it returns an address with no IP, and if that were
// treated as loopback any DoH client could recurse.
func TestUnknownSourceIsRefused(t *testing.T) {
	h, up := aclHandler(t, trustedPolicy(t, "72.52.77.228/32"))

	resp := query(t, h, "www.example.com", &net.TCPAddr{})

	assert.Equal(t, dns.RcodeRefused, resp.Rcode)
	assert.False(t, up.forwarded())
}

func TestIPv6TrustedPrefix(t *testing.T) {
	h, up := aclHandler(t, trustedPolicy(t, "2001:db8::/32"))

	resp := query(t, h, "www.example.com", udpFrom(t, "2001:db8::5"))

	assert.Equal(t, dns.RcodeSuccess, resp.Rcode)
	assert.True(t, up.forwarded())
}

// A dual-stack listener reports a v4 client as ::ffff:a.b.c.d, which must match
// a v4 prefix or every trusted address would need listing twice.
func TestV4MappedSourceMatchesV4Prefix(t *testing.T) {
	h, up := aclHandler(t, trustedPolicy(t, "72.52.77.0/24"))

	resp := query(t, h, "www.example.com", &net.UDPAddr{IP: net.ParseIP("::ffff:72.52.77.228"), Port: 33333})

	assert.Equal(t, dns.RcodeSuccess, resp.Rcode)
	assert.True(t, up.forwarded())
}
