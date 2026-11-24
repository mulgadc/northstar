package backend_test

import (
	"context"
	"log/slog"
	"net"
	"testing"

	"github.com/miekg/dns"
	"github.com/mulgadc/northstar/pkg/backend"
	"github.com/stretchr/testify/require"
)

func benchmarkUpstream(b *testing.B) *backend.Upstream {
	b.Helper()
	return backend.NewUpstream(backend.ParseUpstreamServers([]string{benchServer(b, benchAnswer)}))
}

func benchServer(b *testing.B, h dns.HandlerFunc) string {
	b.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(b, err)
	srv := &dns.Server{PacketConn: pc, Handler: h}
	go func() { _ = srv.ActivateAndServe() }()
	b.Cleanup(func() { _ = srv.Shutdown() })
	return pc.LocalAddr().String()
}

func BenchmarkExchange_HealthyUpstream(b *testing.B) {
	u := benchmarkUpstream(b)
	m := new(dns.Msg)
	m.SetQuestion("bench.example.", dns.TypeA)
	for b.Loop() {
		if _, err := u.Exchange(b.Context(), m); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExchange_HealthyUpstreamParallel(b *testing.B) {
	u := benchmarkUpstream(b)
	b.RunParallel(func(pb *testing.PB) {
		m := new(dns.Msg)
		m.SetQuestion("bench.example.", dns.TypeA)
		for pb.Next() {
			if _, err := u.Exchange(context.Background(), m); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func benchAnswer(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.RecursionAvailable = true
	rr, _ := dns.NewRR(r.Question[0].Name + " 60 IN A 192.0.2.10")
	m.Answer = []dns.RR{rr}
	_ = w.WriteMsg(m)
}

// Three forwarders, the first answering SERVFAIL: the largest supported
// configuration, with ordering, cooldown and retries under contention.
func BenchmarkExchange_ThreeForwardersFirstFailingParallel(b *testing.B) {
	servfail := func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeServerFailure)
		_ = w.WriteMsg(m)
	}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	b.Cleanup(func() { slog.SetDefault(prev) })
	u := backend.NewUpstream(backend.ParseUpstreamServers([]string{
		benchServer(b, servfail), benchServer(b, benchAnswer), benchServer(b, benchAnswer),
	}))
	b.RunParallel(func(pb *testing.PB) {
		m := new(dns.Msg)
		m.SetQuestion("bench.example.", dns.TypeA)
		for pb.Next() {
			if _, err := u.Exchange(context.Background(), m); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
