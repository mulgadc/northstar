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

// HasServers reports whether any upstream forwarder is configured.
func (u *Upstream) HasServers() bool {
	return len(u.Servers) > 0
}

// clientFor builds a DNS client for an upstream server (plaintext or DoT).
func clientFor(server UpstreamServer) *dns.Client {
	if server.UseTLS {
		return &dns.Client{
			Net:       "tcp-tls",
			Timeout:   3 * time.Second,
			TLSConfig: &tls.Config{ServerName: serverNameFromAddr(server.Address)},
		}
	}
	return &dns.Client{Timeout: 3 * time.Second}
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

	var lastErr error
	for _, server := range u.Servers {
		in, _, err := clientFor(server).Exchange(m, server.Address)
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

	var lastErr error

	for _, server := range u.Servers {
		in, _, err := clientFor(server).Exchange(m, server.Address)
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
