package backend

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/miekg/dns"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// tracerName / meterName identify northstar's DNS query-path OTel scope.
const (
	tracerName = "northstar"
	meterName  = "northstar"
)

// instruments holds the DNS query-path tracer and metric instruments for one
// Handler/Upstream pair. A nil *instruments (zero-value Handler{}/Upstream{}
// literals, as used by existing tests) is safe: every method below guards
// against a nil receiver and falls back to no-op behavior.
type instruments struct {
	tracer trace.Tracer

	queries          metric.Int64Counter
	queryDuration    metric.Float64Histogram
	upstreamForwards metric.Int64Counter
	upstreamDuration metric.Float64Histogram
}

// newInstruments builds the shared DNS instruments from the current global
// meter/tracer. Instrument-creation errors are logged at Debug and leave the
// corresponding instrument nil (record calls become no-ops); they never fail
// Handler/Upstream construction.
func newInstruments() *instruments {
	meter := otel.Meter(meterName)
	inst := &instruments{tracer: otel.Tracer(tracerName)}

	var err error
	inst.queries, err = meter.Int64Counter("northstar.dns.queries",
		metric.WithDescription("Count of DNS queries handled."),
		metric.WithUnit("{query}"))
	if err != nil {
		slog.Debug("failed to create northstar.dns.queries instrument", "error", err)
	}

	inst.queryDuration, err = meter.Float64Histogram("northstar.dns.query.duration",
		metric.WithDescription("Duration of handled DNS queries."),
		metric.WithUnit("s"))
	if err != nil {
		slog.Debug("failed to create northstar.dns.query.duration instrument", "error", err)
	}

	inst.upstreamForwards, err = meter.Int64Counter("northstar.dns.upstream.forwards",
		metric.WithDescription("Count of queries forwarded to upstream resolvers."),
		metric.WithUnit("{query}"))
	if err != nil {
		slog.Debug("failed to create northstar.dns.upstream.forwards instrument", "error", err)
	}

	inst.upstreamDuration, err = meter.Float64Histogram("northstar.dns.upstream.duration",
		metric.WithDescription("Duration of upstream forwarder exchanges."),
		metric.WithUnit("s"))
	if err != nil {
		slog.Debug("failed to create northstar.dns.upstream.duration instrument", "error", err)
	}

	return inst
}

// tracer returns the instrument's tracer, or the global "northstar" tracer
// (no-op unless a provider is registered) when the receiver is nil.
func (in *instruments) tracerOrGlobal() trace.Tracer {
	if in != nil && in.tracer != nil {
		return in.tracer
	}
	return otel.Tracer(tracerName)
}

// recordQuery records one handled query on the queries counter and query
// duration histogram. Safe to call with a nil receiver.
func (in *instruments) recordQuery(ctx context.Context, countAttrs, durationAttrs []attribute.KeyValue, elapsed time.Duration) {
	if in == nil {
		return
	}
	if in.queries != nil {
		in.queries.Add(ctx, 1, metric.WithAttributes(countAttrs...))
	}
	if in.queryDuration != nil {
		in.queryDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(durationAttrs...))
	}
}

// recordUpstream records one upstream forwarder attempt on the forwards
// counter and upstream duration histogram. Safe to call with a nil receiver.
func (in *instruments) recordUpstream(ctx context.Context, attrs []attribute.KeyValue, elapsed time.Duration) {
	if in == nil {
		return
	}
	opt := metric.WithAttributes(attrs...)
	if in.upstreamForwards != nil {
		in.upstreamForwards.Add(ctx, 1, opt)
	}
	if in.upstreamDuration != nil {
		in.upstreamDuration.Record(ctx, elapsed.Seconds(), opt)
	}
}

// transportFromWriter reports the wire transport a query arrived on. Listener
// wrappers (see pkg/server) tag their ResponseWriter with Transport() so the
// shared handler can distinguish udp/tcp/tcp-tls/doh; RemoteAddr alone cannot
// (DoT and DoH both present as TCP).
func transportFromWriter(w dns.ResponseWriter) string {
	if t, ok := w.(interface{ Transport() string }); ok {
		return t.Transport()
	}
	if _, ok := w.RemoteAddr().(*net.UDPAddr); ok {
		return "udp"
	}
	return "tcp"
}

// qtypeAttr / rcodeAttr / outcomeAttr are the low-cardinality attribute
// helpers shared by query and upstream metrics/spans. qname is never used as
// a metric label (span attribute only).
func qtypeAttr(qtype uint16) attribute.KeyValue {
	return attribute.String("qtype", dns.TypeToString[qtype])
}

func rcodeAttr(rcode int) attribute.KeyValue {
	return attribute.String("rcode", dns.RcodeToString[rcode])
}

func outcomeAttr(rcode int) attribute.KeyValue {
	outcome := "failure"
	if rcode == dns.RcodeSuccess {
		outcome = "success"
	}
	return attribute.String("outcome", outcome)
}
