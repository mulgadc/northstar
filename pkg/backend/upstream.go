package backend

import (
	"crypto/tls"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// Upstream handles DNS resolution against configurable upstream servers
// with support for both plaintext and TLS (DoT) connections.
type Upstream struct {
	Servers []UpstreamServer
}

type UpstreamServer struct {
	Address string
	UseTLS  bool
}

// NewUpstream creates an upstream resolver from an explicit server list. An
// empty list means no forwarding (non-authoritative queries are refused).
func NewUpstream(servers []UpstreamServer) *Upstream {
	return &Upstream{Servers: servers}
}

// ParseUpstreamServers parses a nameserver list into UpstreamServers. Each entry
// is "host:port" (plaintext) or "tls://host:port" (DoT), e.g.
// "tls://1.1.1.1:853,8.8.8.8:53".
func ParseUpstreamServers(nameservers []string) []UpstreamServer {
	var servers []UpstreamServer
	for _, server := range nameservers {
		server = strings.TrimSpace(server)
		if server == "" {
			continue
		}
		if after, ok := strings.CutPrefix(server, "tls://"); ok {
			servers = append(servers, UpstreamServer{Address: after, UseTLS: true})
		} else {
			servers = append(servers, UpstreamServer{Address: server, UseTLS: false})
		}
	}
	return servers
}

// Timeouts and EDNS sizing for upstream queries. Values track common resolver
// defaults: a 2s connect bound and a 5s reply wait (the resolv.conf `timeout:5`
// default used by glibc/systemd-resolved), with sequential failover to the next
// server. The advertised EDNS0 UDP payload is 1232 bytes, the DNS flag-day 2020
// recommendation also used by unbound/dnsmasq, which avoids most fragmentation
// while still fitting large answers in a single UDP round-trip.
const (
	upstreamDialTimeout = 2 * time.Second
	upstreamReadTimeout = 5 * time.Second
	upstreamUDPBufSize  = 1232
)

// HasServers reports whether any upstream forwarder is configured.
func (u *Upstream) HasServers() bool {
	return len(u.Servers) > 0
}

// udpClient / tcpClient / tlsClient build transport-specific upstream clients.
func udpClient() *dns.Client {
	return &dns.Client{Net: "udp", DialTimeout: upstreamDialTimeout, ReadTimeout: upstreamReadTimeout, WriteTimeout: upstreamDialTimeout}
}

func tcpClient() *dns.Client {
	return &dns.Client{Net: "tcp", DialTimeout: upstreamDialTimeout, ReadTimeout: upstreamReadTimeout, WriteTimeout: upstreamDialTimeout}
}

func tlsClient(server UpstreamServer) *dns.Client {
	return &dns.Client{
		Net:          "tcp-tls",
		DialTimeout:  upstreamDialTimeout,
		ReadTimeout:  upstreamReadTimeout,
		WriteTimeout: upstreamDialTimeout,
		TLSConfig:    &tls.Config{ServerName: serverNameFromAddr(server.Address)},
	}
}

// exchangeServer sends m to a single upstream. Plaintext queries go out over UDP
// first and retry over TCP when the reply is truncated (TC bit), exactly as a
// normal forwarding resolver does; DoT servers use a single TCP-TLS stream.
func exchangeServer(server UpstreamServer, m *dns.Msg) (*dns.Msg, error) {
	if server.UseTLS {
		in, _, err := tlsClient(server).Exchange(m, server.Address)
		return in, err
	}

	in, _, err := udpClient().Exchange(m, server.Address)
	if err != nil {
		return nil, err
	}
	if in != nil && in.Truncated {
		if tin, _, terr := tcpClient().Exchange(m, server.Address); terr == nil && tin != nil {
			return tin, nil
		}
	}
	return in, nil
}

// withUpstreamEDNS sets/raises the outgoing EDNS0 UDP buffer to our advertised
// size so upstream returns full answers regardless of what the client asked for.
func withUpstreamEDNS(m *dns.Msg) {
	if opt := m.IsEdns0(); opt != nil {
		opt.SetUDPSize(upstreamUDPBufSize)
		return
	}
	m.SetEdns0(upstreamUDPBufSize, false)
}

// Exchange forwards a complete query to the upstream servers with failover and
// returns the first successful response. Used to recurse non-authoritative
// names to the configured forwarders.
func (u *Upstream) Exchange(r *dns.Msg) (*dns.Msg, error) {
	if len(u.Servers) == 0 {
		return nil, errors.New("no upstream servers configured")
	}

	m := r.Copy()
	m.RecursionDesired = true
	withUpstreamEDNS(m)

	var lastErr error
	for _, server := range u.Servers {
		in, err := exchangeServer(server, m)
		if err != nil {
			slog.Debug("upstream failed", "server", server.Address, "error", err)
			lastErr = err
			continue
		}
		if in != nil {
			return in, nil
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("all upstream servers failed")
}

// Resolve performs a DNS lookup against upstream servers with failover.
func (u *Upstream) Resolve(name string, qtype uint16) ([]dns.RR, error) {
	m := new(dns.Msg)
	m.Id = dns.Id()
	m.RecursionDesired = true
	m.Question = []dns.Question{{Name: dns.Fqdn(name), Qtype: qtype, Qclass: dns.ClassINET}}
	withUpstreamEDNS(m)

	var lastErr error

	for _, server := range u.Servers {
		in, err := exchangeServer(server, m)
		if err != nil {
			slog.Debug("upstream failed", "server", server.Address, "error", err)
			lastErr = err
			continue
		}

		if in != nil && in.Rcode == dns.RcodeSuccess {
			return in.Answer, nil
		}

		if in != nil {
			lastErr = errors.New(dns.RcodeToString[in.Rcode])
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("all upstream servers failed")
}

func serverNameFromAddr(addr string) string {
	// Extract IP for TLS ServerName (strip port)
	host := addr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		host = addr[:idx]
	}
	return host
}
