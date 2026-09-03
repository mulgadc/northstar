package backend

import (
	"net"
	"net/netip"

	"github.com/miekg/dns"
)

// RecursionPolicy decides which clients may have a non-authoritative query
// forwarded to the upstream resolvers. Authoritative answers are never gated by
// it: a public authoritative server must answer every client, and only the
// recursion path turns that server into an open resolver.
//
// The zero value refuses everyone, so a Handler built without a policy serves
// authoritative data and nothing else.
type RecursionPolicy struct {
	// Public opens recursion to any client, including the internet. Off by
	// default; see UpstreamConfig.AllowRecursion for why.
	Public bool
	// Allowed are the trusted client prefixes, unaffected by Public.
	Allowed []netip.Prefix
}

// NewRecursionPolicy builds a policy from the parsed config, appending loopback
// to the trusted set. Loopback is unconditional because a resolver that cannot
// answer its own host is broken in a way no deployment wants, and no packet
// from off-box can carry a loopback source.
func NewRecursionPolicy(public bool, allowed []netip.Prefix) *RecursionPolicy {
	trusted := make([]netip.Prefix, 0, len(allowed)+2)
	trusted = append(trusted, allowed...)
	trusted = append(trusted,
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	)
	return &RecursionPolicy{Public: public, Allowed: trusted}
}

// Allows reports whether a client may recurse. It fails closed: a client whose
// address cannot be determined is refused rather than trusted, which is what
// makes the DoH path safe (see dohResponseWriter.RemoteAddr).
func (p *RecursionPolicy) Allows(addr netip.Addr) bool {
	if p == nil {
		return false
	}
	if !addr.IsValid() {
		return false
	}
	if p.Public {
		return true
	}
	// A v4-mapped v6 source (::ffff:10.0.0.1, as a dual-stack listener reports)
	// must match a v4 prefix, or every trusted address would need listing twice.
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	for _, prefix := range p.Allowed {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// clientAddr extracts the querying client's address from a ResponseWriter.
// Returns the zero Addr when the transport reports no usable address, which
// Allows treats as untrusted.
func clientAddr(w dns.ResponseWriter) netip.Addr {
	if w == nil {
		return netip.Addr{}
	}
	switch a := w.RemoteAddr().(type) {
	case *net.UDPAddr:
		return addrFromIP(a.IP)
	case *net.TCPAddr:
		return addrFromIP(a.IP)
	default:
		if a == nil {
			return netip.Addr{}
		}
		host, _, err := net.SplitHostPort(a.String())
		if err != nil {
			return netip.Addr{}
		}
		parsed, err := netip.ParseAddr(host)
		if err != nil {
			return netip.Addr{}
		}
		return parsed
	}
}

func addrFromIP(ip net.IP) netip.Addr {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}
	}
	return addr.Unmap()
}
