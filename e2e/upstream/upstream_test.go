// Package upstream runs the real northstar binary against upstream resolvers
// the test controls, and queries it the way a guest resolver does: one UDP
// attempt with a 5 s timeout. It needs no docker and no network.
//
//	NORTHSTAR_E2E_UPSTREAM=1 go test -count=1 -v ./upstream/
//
// NORTHSTAR_E2E_LIVE=1 adds one test against real public resolvers.
package upstream

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

// guestTimeout is a guest resolver's per-attempt wait (glibc and Go both use 5 s).
const guestTimeout = 5 * time.Second

var northstarBin string

func TestMain(m *testing.M) {
	if os.Getenv("NORTHSTAR_E2E_UPSTREAM") == "" {
		fmt.Println("Skipping upstream E2E tests (set NORTHSTAR_E2E_UPSTREAM=1 to run)")
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "northstar-upstream-e2e-")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	northstarBin = filepath.Join(dir, "northstar")
	build := exec.Command("go", "build", "-o", northstarBin, "./cmd/northstar")
	build.Dir = filepath.Join("..", "..")
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Printf("build northstar: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// fakeUpstream is a resolver the test controls. It counts the queries it gets.
type fakeUpstream struct {
	name    string
	addr    string
	queries atomic.Int32
	stop    func()
}

type behaviour func(w dns.ResponseWriter, r *dns.Msg)

// answers is a working recursive resolver: every A query gets 192.0.2.10.
func answers(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.RecursionAvailable = true
	if r.Question[0].Qtype == dns.TypeA {
		rr, _ := dns.NewRR(r.Question[0].Name + " 60 IN A 192.0.2.10")
		m.Answer = []dns.RR{rr}
	}
	_ = w.WriteMsg(m)
}

func rcode(code int) behaviour {
	return func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetRcode(r, code)
		m.RecursionAvailable = code != dns.RcodeRefused
		_ = w.WriteMsg(m)
	}
}

// lame is an authoritative-only server asked for a name it does not serve: a
// referral with RA clear and no answer, as ns1.<zone> gives for google.com.
func lame(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.RecursionAvailable = false
	ns, _ := dns.NewRR("com. 172800 IN NS a.gtld-servers.net.")
	m.Ns = []dns.RR{ns}
	_ = w.WriteMsg(m)
}

func slow(d time.Duration) behaviour {
	return func(w dns.ResponseWriter, r *dns.Msg) {
		time.Sleep(d)
		answers(w, r)
	}
}

func startUpstream(t *testing.T, name string, b behaviour) *fakeUpstream {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	f := &fakeUpstream{name: name, addr: pc.LocalAddr().String()}
	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		f.queries.Add(1)
		b(w, r)
	})}
	go func() { _ = srv.ActivateAndServe() }()
	f.stop = func() { _ = srv.Shutdown() }
	t.Cleanup(f.stop)
	return f
}

// startBlackhole reads queries and never answers, like a dead resolver.
func startBlackhole(t *testing.T, name string) *fakeUpstream {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	f := &fakeUpstream{name: name, addr: pc.LocalAddr().String()}
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
			f.queries.Add(1)
		}
	}()
	f.stop = func() { _ = pc.Close() }
	t.Cleanup(f.stop)
	return f
}

// northstar is one running northstar process and its captured log.
type northstar struct {
	addr string
	mu   sync.Mutex
	log  bytes.Buffer
}

func (n *northstar) logText() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.log.String()
}

func startNorthstar(t *testing.T, upstreams ...string) *northstar {
	t.Helper()
	dir := t.TempDir()
	zones := filepath.Join(dir, "zones")
	require.NoError(t, os.MkdirAll(zones, 0o755))
	zone, err := os.ReadFile(filepath.Join("..", "testdata", "e2etest.net.toml"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(zones, "e2etest.net.toml"), zone, 0o644))

	addr := freeAddr(t)
	quoted := make([]string, len(upstreams))
	for i, u := range upstreams {
		quoted[i] = fmt.Sprintf("%q", u)
	}
	conf := fmt.Sprintf("listen = %q\nzone_dir = %q\n\n[upstream]\nnameservers = [%s]\nallow_recursion_from = [\"127.0.0.1/32\"]\n",
		addr, zones, strings.Join(quoted, ", "))
	path := filepath.Join(dir, "northstar.toml")
	require.NoError(t, os.WriteFile(path, []byte(conf), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, northstarBin, "--config", path)
	n := &northstar{addr: addr}
	stderr, err := cmd.StderrPipe()
	require.NoError(t, err)
	cmd.Stdout = cmd.Stderr
	require.NoError(t, cmd.Start())
	go func() {
		s := bufio.NewScanner(stderr)
		for s.Scan() {
			n.mu.Lock()
			n.log.WriteString(s.Text() + "\n")
			n.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("northstar log:\n%s", n.logText())
		}
	})

	// Ready once it answers its own zone, which never touches an upstream.
	require.Eventually(t, func() bool {
		r, _, err := query(n.addr, "e2etest.net.", time.Second)
		return err == nil && r.Rcode == dns.RcodeSuccess && len(r.Answer) > 0
	}, 15*time.Second, 100*time.Millisecond, "northstar did not start")
	return n
}

func freeAddr(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := pc.LocalAddr().String()
	require.NoError(t, pc.Close())
	return addr
}

// query is one guest-style attempt: UDP, recursion desired, a fixed timeout.
func query(addr, name string, timeout time.Duration) (*dns.Msg, time.Duration, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	m.RecursionDesired = true
	c := &dns.Client{Net: "udp", Timeout: timeout}
	start := time.Now()
	r, _, err := c.Exchange(m, addr)
	return r, time.Since(start), err
}

// guestLookup asserts a guest attempt gets an A answer within within.
func guestLookup(t *testing.T, n *northstar, name string, within time.Duration) {
	t.Helper()
	r, took, err := query(n.addr, name, guestTimeout)
	require.NoErrorf(t, err, "guest lookup of %s failed after %s (a guest resolver gives up at %s)", name, took.Round(time.Millisecond), guestTimeout)
	require.Equalf(t, dns.RcodeSuccess, r.Rcode, "guest lookup of %s: rcode %s after %s", name, dns.RcodeToString[r.Rcode], took.Round(time.Millisecond))
	require.NotEmptyf(t, r.Answer, "guest lookup of %s: no answer", name)
	require.Lessf(t, took, within, "guest lookup of %s took %s", name, took.Round(time.Millisecond))
}

// A dead first upstream must not fail a guest whose second upstream is healthy.
func TestUpstream_DeadFirstUpstream_GuestStillResolves(t *testing.T) {
	dead := startBlackhole(t, "dead")
	ok := startUpstream(t, "ok", answers)
	n := startNorthstar(t, dead.addr, ok.addr)

	guestLookup(t, n, "first.example.", 2*time.Second)
	require.EqualValues(t, 1, ok.queries.Load(), "the healthy upstream did not answer the first lookup")
}

// After one failure the dead upstream is remembered and skipped.
func TestUpstream_DeadUpstreamIsRememberedAndSkipped(t *testing.T) {
	dead := startBlackhole(t, "dead")
	ok := startUpstream(t, "ok", answers)
	n := startNorthstar(t, dead.addr, ok.addr)

	_, _, _ = query(n.addr, "first.example.", guestTimeout)
	before := dead.queries.Load()
	guestLookup(t, n, "second.example.", 200*time.Millisecond)
	require.Equal(t, before, dead.queries.Load(), "the dead upstream was asked again inside its cooldown")
}

// SERVFAIL, REFUSED and a lame referral from the first upstream are failures:
// the guest gets the healthy second upstream's answer.
func TestUpstream_BadAnswerFromFirstUpstreamFailsOver(t *testing.T) {
	for _, tc := range []struct {
		name string
		b    behaviour
	}{
		{"servfail", rcode(dns.RcodeServerFailure)},
		{"refused", rcode(dns.RcodeRefused)},
		{"lame-referral", lame},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := startUpstream(t, tc.name, tc.b)
			ok := startUpstream(t, "ok", answers)
			n := startNorthstar(t, bad.addr, ok.addr)

			guestLookup(t, n, "bad-first.example.", 2*time.Second)
			require.EqualValues(t, 1, bad.queries.Load())
			require.EqualValues(t, 1, ok.queries.Load(), "the healthy upstream was not tried after %s", tc.name)
		})
	}
}

// With every upstream dead the guest gets SERVFAIL before its own timeout,
// not a timeout.
func TestUpstream_AllUpstreamsDead_ServfailBeforeGuestTimeout(t *testing.T) {
	a, b, c := startBlackhole(t, "dead-a"), startBlackhole(t, "dead-b"), startBlackhole(t, "dead-c")
	n := startNorthstar(t, a.addr, b.addr, c.addr)

	r, took, err := query(n.addr, "nothing.example.", guestTimeout)
	require.NoErrorf(t, err, "no answer within the guest's %s (took %s)", guestTimeout, took.Round(time.Millisecond))
	require.Equal(t, dns.RcodeServerFailure, r.Rcode)
	require.Less(t, took, 4500*time.Millisecond)
}

// The last working upstream behind two silent ones still answers in time.
func TestUpstream_LastUpstreamBehindTwoDeadStillResolves(t *testing.T) {
	a, b := startBlackhole(t, "dead-a"), startBlackhole(t, "dead-b")
	ok := startUpstream(t, "ok", answers)
	n := startNorthstar(t, a.addr, b.addr, ok.addr)

	guestLookup(t, n, "third.example.", 4*time.Second)
	require.EqualValues(t, 1, ok.queries.Load())
}

// A slow but working sole upstream still answers: failover speed must not
// exclude the only upstream there is.
func TestUpstream_SlowSoleUpstreamStillAnswers(t *testing.T) {
	s := startUpstream(t, "slow", slow(3*time.Second))
	n := startNorthstar(t, s.addr)

	guestLookup(t, n, "slow.example.", guestTimeout)
}

// An upstream that stops answering is named in the log at Info or above.
func TestUpstream_FailingUpstreamIsLogged(t *testing.T) {
	dead := startBlackhole(t, "dead")
	ok := startUpstream(t, "ok", answers)
	n := startNorthstar(t, dead.addr, ok.addr)

	_, _, _ = query(n.addr, "logged.example.", guestTimeout)
	require.Eventually(t, func() bool {
		return strings.Contains(n.logText(), dead.addr) && strings.Contains(n.logText(), "upstream DNS server failing")
	}, 2*time.Second, 50*time.Millisecond, "no log line names the failing upstream %s", dead.addr)
}

// Live: a dead and a non-recursive server first, then a public resolver.
// Workstation only; never part of CI pass criteria.
func TestUpstream_Live_DeadAndLameFirst(t *testing.T) {
	if os.Getenv("NORTHSTAR_E2E_LIVE") == "" {
		t.Skip("set NORTHSTAR_E2E_LIVE=1 to query real resolvers")
	}
	lameNS := os.Getenv("NORTHSTAR_E2E_LAME")
	if lameNS == "" {
		lameNS = "216.239.32.10:53" // ns1.google.com: REFUSED for names outside google.com
	}
	n := startNorthstar(t, "192.0.2.1:53", lameNS, "1.1.1.1:53")
	// 4 s: a silent server such as a broken router costs a full attempt.
	guestLookup(t, n, "example.org.", 4*time.Second)
	guestLookup(t, n, "example.com.", 500*time.Millisecond)
}
