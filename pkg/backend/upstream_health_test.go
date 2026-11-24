package backend_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/mulgadc/northstar/pkg/backend"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fake is an in-process upstream whose behaviour the test can change.
type fake struct {
	addr     string
	queries  atomic.Int32
	mu       sync.Mutex
	behave   func(w dns.ResponseWriter, r *dns.Msg)
	received chan struct{}
}

func (f *fake) set(b func(w dns.ResponseWriter, r *dns.Msg)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.behave = b
}

func startFake(t *testing.T, b func(w dns.ResponseWriter, r *dns.Msg)) *fake {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	f := &fake{addr: pc.LocalAddr().String(), behave: b, received: make(chan struct{}, 64)}
	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		f.queries.Add(1)
		f.received <- struct{}{}
		f.mu.Lock()
		h := f.behave
		f.mu.Unlock()
		h(w, r)
	})}
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	return f
}

func answerA(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.RecursionAvailable = true
	rr, _ := dns.NewRR(r.Question[0].Name + " 60 IN A 192.0.2.10")
	m.Answer = []dns.RR{rr}
	_ = w.WriteMsg(m)
}

func answerRcode(code int) func(w dns.ResponseWriter, r *dns.Msg) {
	return func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetRcode(r, code)
		m.RecursionAvailable = true
		_ = w.WriteMsg(m)
	}
}

// never answers; the query is still counted and signalled.
func never(dns.ResponseWriter, *dns.Msg) {}

// hold blocks the reply until release is closed, then answers.
func hold(release <-chan struct{}) func(w dns.ResponseWriter, r *dns.Msg) {
	return func(w dns.ResponseWriter, r *dns.Msg) {
		<-release
		answerA(w, r)
	}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newUpstream(t *testing.T, fakes ...*fake) (*backend.Upstream, *clock) {
	t.Helper()
	addrs := make([]string, len(fakes))
	for i, f := range fakes {
		addrs[i] = f.addr
	}
	u := backend.NewUpstream(backend.ParseUpstreamServers(addrs))
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	backend.SetClock(u, c.now)
	return u, c
}

func ask(t *testing.T, u *backend.Upstream, name string) (*dns.Msg, error) {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	return u.Exchange(t.Context(), m)
}

func requireAnswer(t *testing.T, u *backend.Upstream, name string) {
	t.Helper()
	resp, err := ask(t, u, name)
	require.NoError(t, err)
	require.Equal(t, dns.RcodeSuccess, resp.Rcode)
	require.NotEmpty(t, resp.Answer)
}

// captureLogs sends slog output to a buffer for the rest of the test.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) count(substr string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Count(s.b.String(), substr)
}

func TestExchange_FailedUpstreamIsSkippedUntilCooldown(t *testing.T) {
	bad := startFake(t, answerRcode(dns.RcodeServerFailure))
	ok := startFake(t, answerA)
	u, clk := newUpstream(t, bad, ok)

	requireAnswer(t, u, "a.example.")
	require.EqualValues(t, 1, bad.queries.Load())
	assert.Equal(t, 1, backend.Fails(u, bad.addr))

	clk.advance(9 * time.Second)
	requireAnswer(t, u, "b.example.")
	require.EqualValues(t, 1, bad.queries.Load(), "asked inside its cooldown")

	clk.advance(2 * time.Second)
	requireAnswer(t, u, "c.example.")
	require.EqualValues(t, 2, bad.queries.Load(), "not retried after its cooldown")
	assert.Equal(t, 2, backend.Fails(u, bad.addr))
	assert.EqualValues(t, 3, ok.queries.Load())
}

func TestExchange_RecoveryWithBackupHealthy(t *testing.T) {
	first := startFake(t, answerRcode(dns.RcodeServerFailure))
	backup := startFake(t, answerA)
	u, clk := newUpstream(t, first, backup)

	requireAnswer(t, u, "a.example.")
	first.set(answerA)
	clk.advance(11 * time.Second)

	requireAnswer(t, u, "b.example.")
	assert.EqualValues(t, 2, first.queries.Load())
	assert.EqualValues(t, 1, backup.queries.Load(), "the recovered first upstream did not take its place back")
	assert.Equal(t, 0, backend.Fails(u, first.addr))

	requireAnswer(t, u, "c.example.")
	assert.EqualValues(t, 3, first.queries.Load())
	assert.EqualValues(t, 1, backup.queries.Load())
}

func TestExchange_OnlyOneQueryTakesTheRecoveryTrial(t *testing.T) {
	for _, tc := range []struct {
		name       string
		backupDown bool
	}{
		{"backup healthy", false},
		{"every other upstream in cooldown", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			first := startFake(t, hold(release))
			backup := startFake(t, answerA)
			u, clk := newUpstream(t, first, backup)
			now := clk.now()
			backend.SetHealth(u, first.addr, 1, now.Add(-time.Second))
			if tc.backupDown {
				backend.SetHealth(u, backup.addr, 1, now.Add(10*time.Second))
			}

			trial := make(chan error, 1)
			go func() {
				_, err := ask(t, u, "trial.example.")
				trial <- err
			}()
			<-first.received

			var wg sync.WaitGroup
			for range 5 {
				wg.Go(func() {
					resp, err := ask(t, u, "other.example.")
					assert.NoError(t, err)
					if err == nil {
						assert.NotEmpty(t, resp.Answer)
					}
				})
			}
			wg.Wait()
			assert.EqualValues(t, 1, first.queries.Load(), "a second query reached the upstream under trial")
			assert.EqualValues(t, 5, backup.queries.Load())

			close(release)
			require.NoError(t, <-trial)
			assert.Equal(t, 0, backend.Fails(u, first.addr))
		})
	}
}

func TestExchange_CancelAfterUpstreamReceivedQueryIsNotAFailure(t *testing.T) {
	silent := startFake(t, never)
	ok := startFake(t, answerA)
	u, _ := newUpstream(t, silent, ok)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-silent.received
		cancel()
	}()
	m := new(dns.Msg)
	m.SetQuestion("gone.example.", dns.TypeA)
	start := time.Now()
	_, err := u.Exchange(ctx, m)
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 500*time.Millisecond, "the cancelled attempt was not interrupted")
	assert.Equal(t, 0, backend.Fails(u, silent.addr), "a caller's cancel counted against the upstream")
	assert.EqualValues(t, 0, ok.queries.Load(), "the walk went on after the caller left")
}

func TestExchange_EveryUpstreamRefusedReturnsRefused(t *testing.T) {
	a := startFake(t, answerRcode(dns.RcodeRefused))
	b := startFake(t, answerRcode(dns.RcodeRefused))
	u, _ := newUpstream(t, a, b)

	resp, err := ask(t, u, "policy.example.")
	require.NoError(t, err)
	assert.Equal(t, dns.RcodeRefused, resp.Rcode)

	c := startFake(t, answerRcode(dns.RcodeRefused))
	d := startFake(t, answerRcode(dns.RcodeServerFailure))
	u, _ = newUpstream(t, c, d)
	_, err = ask(t, u, "mixed.example.")
	require.Error(t, err, "a refusal and a SERVFAIL must give SERVFAIL, not REFUSED")
}

func TestExchange_LogsFailingOnceAndRecoveredOnce(t *testing.T) {
	logs := captureLogs(t)
	sole := startFake(t, answerRcode(dns.RcodeServerFailure))
	u, clk := newUpstream(t, sole)

	for range 3 {
		_, err := ask(t, u, "down.example.")
		require.Error(t, err)
		clk.advance(time.Second)
	}
	sole.set(answerA)
	requireAnswer(t, u, "up.example.")
	requireAnswer(t, u, "up2.example.")

	assert.Equal(t, 1, logs.count("upstream DNS server failing"))
	assert.Equal(t, 1, logs.count("upstream DNS server recovered"))
	assert.Equal(t, 1, logs.count("all upstream DNS servers failed"), "the all-failed warning is not rate limited")
	assert.Equal(t, 0, backend.Fails(u, sole.addr))
}

func TestExchange_NegativeAnswersAreAnswers(t *testing.T) {
	for _, code := range []int{dns.RcodeNameError, dns.RcodeSuccess} {
		first := startFake(t, func(w dns.ResponseWriter, r *dns.Msg) {
			m := new(dns.Msg)
			m.SetRcode(r, code)
			m.RecursionAvailable = true
			soa, _ := dns.NewRR("example. 300 IN SOA ns.example. host.example. 1 7200 3600 1209600 300")
			m.Ns = []dns.RR{soa}
			_ = w.WriteMsg(m)
		})
		backup := startFake(t, answerA)
		u, _ := newUpstream(t, first, backup)

		resp, err := ask(t, u, "missing.example.")
		require.NoError(t, err)
		assert.Equal(t, code, resp.Rcode)
		assert.EqualValues(t, 0, backup.queries.Load(), "%s was treated as a failure", dns.RcodeToString[code])
		assert.Equal(t, 0, backend.Fails(u, first.addr))
	}
}

func TestExchange_OnlyFirstThreeForwardersAreUsed(t *testing.T) {
	var bad []*fake
	for range 3 {
		bad = append(bad, startFake(t, answerRcode(dns.RcodeServerFailure)))
	}
	fourth := startFake(t, answerA)
	u, _ := newUpstream(t, append(bad, fourth)...)

	_, err := ask(t, u, "four.example.")
	require.Error(t, err)
	assert.EqualValues(t, 0, fourth.queries.Load())
}

// Two forwarders leave cooldown together and the first is still dead: while
// one query waits on it, others still get the recovered second one.
func TestExchange_TrialIsClaimedOnlyWhenContacted(t *testing.T) {
	release := make(chan struct{})
	first := startFake(t, hold(release))
	second := startFake(t, answerA)
	third := startFake(t, answerA)
	u, clk := newUpstream(t, first, second, third)
	now := clk.now()
	backend.SetHealth(u, first.addr, 1, now.Add(-time.Second))
	backend.SetHealth(u, second.addr, 1, now.Add(-time.Second))
	backend.SetHealth(u, third.addr, 1, now.Add(10*time.Second))

	trial := make(chan error, 1)
	go func() {
		_, err := ask(t, u, "trial.example.")
		trial <- err
	}()
	<-first.received

	for range 3 {
		requireAnswer(t, u, "other.example.")
	}
	assert.EqualValues(t, 1, first.queries.Load(), "a second query reached the forwarder under trial")
	assert.EqualValues(t, 0, third.queries.Load(), "the recovered second forwarder was held by the other query's trial")
	close(release)
	require.NoError(t, <-trial)
}

// startTruncating answers over UDP with TC set and no answer; nothing listens
// on TCP at that port, so the retry over TCP fails.
func startTruncating(t *testing.T) *fake {
	t.Helper()
	return startFake(t, func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.RecursionAvailable = true
		m.Truncated = true
		_ = w.WriteMsg(m)
	})
}

func TestExchange_FailedTCPRetryFailsOver(t *testing.T) {
	trunc := startTruncating(t)
	ok := startFake(t, answerA)
	u, _ := newUpstream(t, trunc, ok)

	requireAnswer(t, u, "big.example.")
	assert.EqualValues(t, 1, ok.queries.Load())
	assert.Equal(t, 1, backend.Fails(u, trunc.addr))
}

func TestNewUpstream_InvalidAddressIsDroppedAndNotLogged(t *testing.T) {
	logs := captureLogs(t)
	ok := startFake(t, answerA)
	u := backend.NewUpstream(backend.ParseUpstreamServers([]string{"user:s3cret@192.0.2.1:53", "192.0.2.1", ok.addr}))

	assert.Equal(t, []string{ok.addr}, u.Addresses())
	assert.Equal(t, 2, logs.count("upstream nameserver is not host:port, ignored"))
	assert.Zero(t, logs.count("s3cret"))
}
