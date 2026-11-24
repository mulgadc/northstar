package backend

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Upstream forwards recursive queries to the configured forwarders, plaintext
// or TLS (DoT). It remembers which forwarders are failing and asks the healthy
// ones first, so one dead forwarder does not fail every lookup.
type Upstream struct {
	servers []*upstreamHealth

	// mu guards every upstreamHealth and lastAllFailed.
	mu            sync.Mutex
	lastAllFailed time.Time
	now           func() time.Time

	// inst is wired in by backend.NewHandler so upstream spans/metrics share
	// the Handler's tracer/meter. A nil inst (Upstream built via NewUpstream
	// directly, as in tests) means no-op spans/metrics.
	inst *instruments
}

type UpstreamServer struct {
	Address string
	UseTLS  bool
}

// upstreamHealth is one forwarder and its failure score. fails counts
// consecutive failures; a failure moves the forwarder behind the others until
// downUntil. trial is set while one query retries it after its cooldown.
type upstreamHealth struct {
	UpstreamServer

	fails        int
	downUntil    time.Time
	failingSince time.Time
	trial        bool
}

// A guest resolver waits 5 s per attempt, so a query is bounded below that.
// Each forwarder but the last gets attemptBudget; the last gets what is left.
// cooldown matches the guest DNS shim's backend cooldown.
const (
	queryBudget    = 4 * time.Second
	attemptBudget  = 1500 * time.Millisecond
	cooldown       = 10 * time.Second
	maxForwarders  = 3
	upstreamUDPBuf = 1232 // DNS flag-day 2020 EDNS0 payload, as unbound/dnsmasq use
)

// NewUpstream creates an upstream resolver from an explicit server list. An
// empty list means no forwarding (non-authoritative queries are refused).
// Only the first maxForwarders servers are used.
func NewUpstream(servers []UpstreamServer) *Upstream {
	if len(servers) > maxForwarders {
		slog.Warn("upstream nameservers beyond the first 3 are ignored",
			"ignored", addresses(servers[maxForwarders:]))
		servers = servers[:maxForwarders]
	}
	u := &Upstream{now: time.Now}
	for _, s := range servers {
		u.servers = append(u.servers, &upstreamHealth{UpstreamServer: s})
	}
	return u
}

func addresses(servers []UpstreamServer) []string {
	out := make([]string, len(servers))
	for i, s := range servers {
		out[i] = s.Address
		if s.UseTLS {
			out[i] = "tls://" + s.Address
		}
	}
	return out
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

// Addresses lists the forwarders in use, in config order.
func (u *Upstream) Addresses() []string {
	servers := make([]UpstreamServer, len(u.servers))
	for i, s := range u.servers {
		servers[i] = s.UpstreamServer
	}
	return addresses(servers)
}

// HasServers reports whether any upstream forwarder is configured.
func (u *Upstream) HasServers() bool {
	return len(u.servers) > 0
}

// Clients carry the whole query budget as their timeout; the per-attempt
// context deadline is what bounds each exchange.
func udpClient() *dns.Client {
	return &dns.Client{Net: "udp", Timeout: queryBudget}
}

func tcpClient() *dns.Client {
	return &dns.Client{Net: "tcp", Timeout: queryBudget}
}

func tlsClient(server UpstreamServer) *dns.Client {
	return &dns.Client{
		Net:       "tcp-tls",
		Timeout:   queryBudget,
		TLSConfig: &tls.Config{ServerName: serverNameFromAddr(server.Address)},
	}
}

// exchangeServer sends m to a single upstream within ctx. Plaintext queries go
// out over UDP and retry over TCP when the reply is truncated (TC bit); DoT
// servers use a single TCP-TLS stream.
func exchangeServer(ctx context.Context, server UpstreamServer, m *dns.Msg) (*dns.Msg, error) {
	if server.UseTLS {
		return exchangeConn(ctx, tlsClient(server), server.Address, m)
	}
	in, err := exchangeConn(ctx, udpClient(), server.Address, m)
	if err != nil {
		return nil, err
	}
	if in.Truncated {
		if tin, terr := exchangeConn(ctx, tcpClient(), server.Address, m); terr == nil {
			return tin, nil
		}
	}
	return in, nil
}

// exchangeConn dials its own socket so a cancelled ctx closes it and ends a
// blocked read at once; the ctx deadline bounds the dial, write and read.
func exchangeConn(ctx context.Context, c *dns.Client, addr string, m *dns.Msg) (*dns.Msg, error) {
	conn, err := c.DialContext(ctx, addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	in, _, err := c.ExchangeWithConnContext(ctx, m, conn)
	return in, err
}

// withUpstreamEDNS sets/raises the outgoing EDNS0 UDP buffer to our advertised
// size so upstream returns full answers regardless of what the client asked for.
func withUpstreamEDNS(m *dns.Msg) {
	if opt := m.IsEdns0(); opt != nil {
		opt.SetUDPSize(upstreamUDPBuf)
		return
	}
	m.SetEdns0(upstreamUDPBuf, false)
}

// exchangeAndRecord runs exchangeServer for one upstream server wrapped in a
// child span and the upstream forwards/duration metrics. spanName
// distinguishes the full-query forwarder path (dns.upstream.exchange) from
// the CNAME-chase resolver path (dns.upstream.resolve).
func (u *Upstream) exchangeAndRecord(ctx context.Context, spanName string, server UpstreamServer, m *dns.Msg) (*dns.Msg, error) {
	ctx, span := u.inst.tracerOrGlobal().Start(ctx, spanName, trace.WithAttributes(attribute.String("server", server.Address)))
	defer span.End()

	start := time.Now()
	in, err := exchangeServer(ctx, server, m)
	elapsed := time.Since(start)

	outcome := "success"
	switch {
	case err != nil:
		outcome = "failure"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	case in != nil:
		span.SetAttributes(attribute.String("rcode", dns.RcodeToString[in.Rcode]))
		if in.Rcode != dns.RcodeSuccess {
			outcome = "failure"
		}
	}

	u.inst.recordUpstream(ctx, []attribute.KeyValue{
		attribute.String("server", server.Address),
		attribute.String("outcome", outcome),
	}, elapsed)

	return in, err
}

// failureReason says why a reply is not an answer, or "" if it is. NOERROR
// and NXDOMAIN are answers; a referral without recursion available comes from
// a server that will not recurse for us.
func failureReason(in *dns.Msg, err error) string {
	if err != nil {
		return err.Error()
	}
	switch in.Rcode {
	case dns.RcodeServerFailure, dns.RcodeRefused, dns.RcodeNotImplemented, dns.RcodeFormatError:
		return dns.RcodeToString[in.Rcode]
	case dns.RcodeSuccess:
		if !in.RecursionAvailable && len(in.Answer) == 0 && slices.ContainsFunc(in.Ns, func(rr dns.RR) bool {
			return rr.Header().Rrtype == dns.TypeNS
		}) {
			return "referral without recursion"
		}
	}
	return ""
}

// order returns the forwarders to try for one query: those not in cooldown in
// config order, then those in cooldown, lowest score first. One query at a
// time retries a forwarder whose cooldown has ended; it is returned in claimed.
func (u *Upstream) order() (order, claimed []*upstreamHealth) {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	shared := len(u.servers) > 1
	var cooling []*upstreamHealth
	for _, s := range u.servers {
		switch {
		case s.trial && shared:
		case now.Before(s.downUntil):
			cooling = append(cooling, s)
		default:
			if s.fails > 0 && shared {
				s.trial = true
				claimed = append(claimed, s)
			}
			order = append(order, s)
		}
	}
	slices.SortStableFunc(cooling, func(a, b *upstreamHealth) int { return cmp.Compare(a.fails, b.fails) })
	order = append(order, cooling...)
	if len(order) == 0 {
		// Every forwarder is in another query's trial: ask them anyway.
		order = slices.Clone(u.servers)
	}
	return order, claimed
}

func (u *Upstream) releaseTrials(claimed []*upstreamHealth) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, s := range claimed {
		s.trial = false
	}
}

func (u *Upstream) markFailed(s *upstreamHealth, reason string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	s.fails++
	s.downUntil = now.Add(cooldown)
	if s.fails == 1 {
		s.failingSince = now
		slog.Info("upstream DNS server failing", "server", s.Address, "reason", reason, "consecutive_failures", s.fails)
	}
}

func (u *Upstream) markOK(s *upstreamHealth) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if s.fails == 0 {
		return
	}
	slog.Info("upstream DNS server recovered", "server", s.Address,
		"failed_for", u.now().Sub(s.failingSince).Round(time.Millisecond).String())
	s.fails = 0
	s.downUntil = time.Time{}
}

func (u *Upstream) warnAllFailed(reasons []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	if !u.lastAllFailed.IsZero() && now.Sub(u.lastAllFailed) < cooldown {
		return
	}
	u.lastAllFailed = now
	slog.Warn("all upstream DNS servers failed", "reasons", reasons)
}

// Exchange forwards a complete query to the forwarders, healthiest first, and
// returns the first answer within queryBudget. If every forwarder refused it
// returns REFUSED; otherwise an error, which the handler answers as SERVFAIL.
// A caller that gives up does not count against the forwarder.
func (u *Upstream) Exchange(ctx context.Context, r *dns.Msg) (*dns.Msg, error) {
	if len(u.servers) == 0 {
		return nil, errors.New("no upstream servers configured")
	}

	m := r.Copy()
	m.RecursionDesired = true
	withUpstreamEDNS(m)

	qctx, cancel := context.WithTimeout(ctx, queryBudget)
	defer cancel()
	order, claimed := u.order()
	defer u.releaseTrials(claimed)

	var refused *dns.Msg
	allRefused := len(order) == len(u.servers)
	var reasons []string
	for i, s := range order {
		// The last forwarder gets all that is left of the query budget.
		actx, acancel := qctx, context.CancelFunc(func() {})
		if i < len(order)-1 {
			actx, acancel = context.WithTimeout(qctx, attemptBudget)
		}
		start := time.Now()
		in, err := u.exchangeAndRecord(actx, "dns.upstream.exchange", s.UpstreamServer, m)
		timedOut := err != nil && actx.Err() != nil
		acancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		reason := failureReason(in, err)
		if reason == "" {
			u.markOK(s)
			return in, nil
		}
		if timedOut {
			reason = fmt.Sprintf("no reply within %s", time.Since(start).Round(100*time.Millisecond))
		}
		u.markFailed(s, reason)
		reasons = append(reasons, s.Address+": "+reason)
		if in != nil && in.Rcode == dns.RcodeRefused {
			refused = in
		} else {
			allRefused = false
		}
		if qctx.Err() != nil {
			break
		}
	}

	u.warnAllFailed(reasons)
	if allRefused && len(reasons) == len(u.servers) {
		return refused, nil
	}
	return nil, errors.New("all upstream servers failed")
}

// Resolve performs a DNS lookup against upstream servers with failover.
func (u *Upstream) Resolve(ctx context.Context, name string, qtype uint16) ([]dns.RR, error) {
	m := new(dns.Msg)
	m.Id = dns.Id()
	m.RecursionDesired = true
	m.Question = []dns.Question{{Name: dns.Fqdn(name), Qtype: qtype, Qclass: dns.ClassINET}}
	withUpstreamEDNS(m)

	var lastErr error

	for _, server := range u.servers {
		actx, cancel := context.WithTimeout(ctx, queryBudget)
		in, err := u.exchangeAndRecord(actx, "dns.upstream.resolve", server.UpstreamServer, m)
		cancel()
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
	if before, _, ok := strings.CutLast(addr, ":"); ok {
		host = before
	}
	return host
}
