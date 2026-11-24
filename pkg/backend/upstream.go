package backend

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strconv"
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
	afterOrder    func()

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
	servers = validServers(servers)
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

// validServers drops entries that are not host:port, naming them only by
// position so a mistyped credential never reaches the log.
func validServers(servers []UpstreamServer) []UpstreamServer {
	valid := make([]UpstreamServer, 0, len(servers))
	for i, s := range servers {
		host, port, err := net.SplitHostPort(s.Address)
		if err != nil || !validHost(host) {
			slog.Warn("upstream nameserver is not host:port, ignored", "index", i)
			continue
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			slog.Warn("upstream nameserver is not host:port, ignored", "index", i)
			continue
		}
		valid = append(valid, s)
	}
	return valid
}

// validHost accepts an IP address, including a scoped IPv6 one, or a DNS name.
func validHost(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	return host != "" && !strings.ContainsFunc(host, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			return false
		}
		return true
	})
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
		return exchangeConn(ctx, tcpClient(), server.Address, m)
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

type exchangeFunc func(ctx context.Context, server UpstreamServer, m *dns.Msg) (*dns.Msg, error)

// resolveExchange is Resolve's transport: a 2 s dial and 5 s read per server,
// and the truncated UDP reply when the TCP retry fails.
func resolveExchange(_ context.Context, server UpstreamServer, m *dns.Msg) (*dns.Msg, error) {
	client := func(network string) *dns.Client {
		return &dns.Client{Net: network, DialTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 2 * time.Second}
	}
	if server.UseTLS {
		c := client("tcp-tls")
		c.TLSConfig = &tls.Config{ServerName: serverNameFromAddr(server.Address)}
		in, _, err := c.Exchange(m, server.Address)
		return in, err
	}
	in, _, err := client("udp").Exchange(m, server.Address)
	if err != nil {
		return nil, err
	}
	if in != nil && in.Truncated {
		if tin, _, terr := client("tcp").Exchange(m, server.Address); terr == nil && tin != nil {
			return tin, nil
		}
	}
	return in, nil
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

// exchangeAndRecord runs one exchange in a child span (dns.upstream.exchange
// for forwarded queries, dns.upstream.resolve for CNAME chasing) and records
// the upstream forwards/duration metrics.
func (u *Upstream) exchangeAndRecord(ctx context.Context, spanName string, exchange exchangeFunc, server UpstreamServer, m *dns.Msg) (*dns.Msg, error) {
	ctx, span := u.inst.tracerOrGlobal().Start(ctx, spanName, trace.WithAttributes(attribute.String("server", server.Address)))
	defer span.End()

	start := time.Now()
	in, err := exchange(ctx, server, m)
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

// failureReason says why a reply is not an answer, or "" if it is. A REFUSED
// or referral with recursion unavailable comes from a server that will not
// recurse for us; a recursive resolver's REFUSED is its policy answer.
func failureReason(in *dns.Msg, err error) string {
	if err != nil {
		return err.Error()
	}
	switch in.Rcode {
	case dns.RcodeServerFailure, dns.RcodeNotImplemented, dns.RcodeFormatError:
		return dns.RcodeToString[in.Rcode]
	case dns.RcodeRefused:
		if !in.RecursionAvailable {
			return "REFUSED without recursion"
		}
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
// config order, then those in cooldown, lowest score first. A forwarder that
// another query is retrying after its cooldown is left out.
func (u *Upstream) order() []*upstreamHealth {
	u.mu.Lock()
	hook := u.afterOrder
	order := u.orderLocked()
	u.mu.Unlock()
	if hook != nil {
		hook()
	}
	return order
}

func (u *Upstream) orderLocked() []*upstreamHealth {
	now := u.now()
	var order, cooling []*upstreamHealth
	for _, s := range u.servers {
		switch {
		case s.trial:
		case now.Before(s.downUntil):
			cooling = append(cooling, s)
		default:
			order = append(order, s)
		}
	}
	slices.SortStableFunc(cooling, func(a, b *upstreamHealth) int { return cmp.Compare(a.fails, b.fails) })
	return append(order, cooling...)
}

// claim runs just before contacting s: false if another query is retrying s.
// It claims the retry of a forwarder out of cooldown unless all others are
// under trial; last is true when none of rest can be contacted.
func (u *Upstream) claim(s *upstreamHealth, rest []*upstreamHealth) (ok, trial, last bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if s.trial {
		return false, false, false
	}
	others := slices.ContainsFunc(u.servers, func(o *upstreamHealth) bool { return o != s && !o.trial })
	if s.fails > 0 && !u.now().Before(s.downUntil) && others {
		s.trial = true
		trial = true
	}
	last = !slices.ContainsFunc(rest, func(r *upstreamHealth) bool { return !r.trial })
	return true, trial, last
}

// record applies one attempt's outcome to s and ends its trial. The log line
// is returned, not written, so a slow log sink never holds the mutex.
func (u *Upstream) record(s *upstreamHealth, trial bool, reason string) func() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if trial {
		s.trial = false
	}
	now := u.now()
	if reason == "" {
		if s.fails == 0 {
			return func() {}
		}
		failedFor := now.Sub(s.failingSince).Round(time.Millisecond).String()
		s.fails, s.downUntil = 0, time.Time{}
		return func() { slog.Info("upstream DNS server recovered", "server", s.Address, "failed_for", failedFor) }
	}
	s.fails++
	s.downUntil = now.Add(cooldown)
	if s.fails > 1 {
		return func() {}
	}
	s.failingSince = now
	return func() {
		slog.Info("upstream DNS server failing", "server", s.Address, "reason", reason, "consecutive_failures", 1)
	}
}

func (u *Upstream) release(s *upstreamHealth) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s.trial = false
}

// allFailedWarnDue reports whether the all-failed Warn is due (once per cooldown).
func (u *Upstream) allFailedWarnDue() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	if !u.lastAllFailed.IsZero() && now.Sub(u.lastAllFailed) < cooldown {
		return false
	}
	u.lastAllFailed = now
	return true
}

// Exchange returns the first answer from the forwarders, healthiest first,
// within queryBudget: REFUSED if every forwarder refused, else an error the
// handler answers as SERVFAIL. A caller giving up is not a forwarder failure.
func (u *Upstream) Exchange(ctx context.Context, r *dns.Msg) (*dns.Msg, error) {
	if len(u.servers) == 0 {
		return nil, errors.New("no upstream servers configured")
	}

	m := r.Copy()
	m.RecursionDesired = true
	withUpstreamEDNS(m)

	qctx, cancel := context.WithTimeout(ctx, queryBudget)
	defer cancel()
	order := u.order()

	var refused *dns.Msg
	var reasons []string
	refusals := 0
	for i, s := range order {
		ok, trial, last := u.claim(s, order[i+1:])
		if !ok {
			continue
		}
		// The last forwarder gets all that is left of the query budget.
		actx, acancel := qctx, context.CancelFunc(func() {})
		if !last {
			actx, acancel = context.WithTimeout(qctx, attemptBudget)
		}
		start := time.Now()
		in, err := u.exchangeAndRecord(actx, "dns.upstream.exchange", exchangeServer, s.UpstreamServer, m)
		timedOut := err != nil && actx.Err() != nil
		acancel()
		if ctx.Err() != nil {
			if trial {
				u.release(s)
			}
			return nil, ctx.Err()
		}

		reason := failureReason(in, err)
		if timedOut {
			reason = fmt.Sprintf("no reply within %s", time.Since(start).Round(100*time.Millisecond))
		}
		u.record(s, trial, reason)()
		if reason == "" {
			return in, nil
		}
		reasons = append(reasons, s.Address+": "+reason)
		if in != nil && in.Rcode == dns.RcodeRefused {
			refused = in
			refusals++
		}
		if qctx.Err() != nil {
			break
		}
	}

	if u.allFailedWarnDue() {
		slog.Warn("all upstream DNS servers failed", "reasons", reasons)
	}
	if refusals == len(u.servers) {
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
		in, err := u.exchangeAndRecord(ctx, "dns.upstream.resolve", resolveExchange, server.UpstreamServer, m)
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
