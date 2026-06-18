package backend

import (
	"crypto/tls"
	"errors"
	"log/slog"
	"os"
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

// NewUpstream creates an upstream resolver from the NORTHSTAR_UPSTREAM env var.
// Format: "tls://1.1.1.1:853,tls://8.8.8.8:853,1.1.1.1:53"
// Falls back to Cloudflare and Google DoT if not configured.
func NewUpstream() *Upstream {
	upstream := &Upstream{}

	envUpstream := os.Getenv("NORTHSTAR_UPSTREAM")
	if envUpstream != "" {
		for _, server := range strings.Split(envUpstream, ",") {
			server = strings.TrimSpace(server)
			if strings.HasPrefix(server, "tls://") {
				upstream.Servers = append(upstream.Servers, UpstreamServer{
					Address: strings.TrimPrefix(server, "tls://"),
					UseTLS:  true,
				})
			} else {
				upstream.Servers = append(upstream.Servers, UpstreamServer{
					Address: server,
					UseTLS:  false,
				})
			}
		}
	} else {
		// Defaults: TLS first, plaintext fallback
		upstream.Servers = []UpstreamServer{
			{Address: "1.1.1.1:853", UseTLS: true},
			{Address: "8.8.8.8:853", UseTLS: true},
			{Address: "1.1.1.1:53", UseTLS: false},
		}
	}

	return upstream
}

// Resolve performs a DNS lookup against upstream servers with failover.
func (u *Upstream) Resolve(name string, qtype uint16) ([]dns.RR, error) {
	m := new(dns.Msg)
	m.Id = dns.Id()
	m.RecursionDesired = true
	m.Question = []dns.Question{{Name: dns.Fqdn(name), Qtype: qtype, Qclass: dns.ClassINET}}

	var lastErr error

	for _, server := range u.Servers {
		var client *dns.Client

		if server.UseTLS {
			client = &dns.Client{
				Net:     "tcp-tls",
				Timeout: 3 * time.Second,
				TLSConfig: &tls.Config{
					ServerName: serverNameFromAddr(server.Address),
				},
			}
		} else {
			client = &dns.Client{
				Timeout: 3 * time.Second,
			}
		}

		in, _, err := client.Exchange(m, server.Address)
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
