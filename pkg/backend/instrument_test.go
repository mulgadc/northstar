package backend_test

import (
	"context"
	"net"
	"testing"

	"github.com/miekg/dns"
	"github.com/mulgadc/northstar/pkg/backend"
	"github.com/mulgadc/northstar/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// fakeResponseWriter is a minimal in-memory dns.ResponseWriter used to drive
// Handler.ServeDNS without a real network listener.
type fakeResponseWriter struct {
	msg *dns.Msg
}

var _ dns.ResponseWriter = (*fakeResponseWriter)(nil)

func (f *fakeResponseWriter) WriteMsg(m *dns.Msg) error { f.msg = m; return nil }
func (f *fakeResponseWriter) Write(b []byte) (int, error) {
	m := new(dns.Msg)
	if err := m.Unpack(b); err != nil {
		return 0, err
	}
	f.msg = m
	return len(b), nil
}
func (f *fakeResponseWriter) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53}
}
func (f *fakeResponseWriter) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
}
func (f *fakeResponseWriter) Close() error        { return nil }
func (f *fakeResponseWriter) TsigStatus() error   { return nil }
func (f *fakeResponseWriter) TsigTimersOnly(bool) {}
func (f *fakeResponseWriter) Hijack()             {}

// setupTestProviders installs a manual-reader MeterProvider and a recording
// TracerProvider for the test, restoring the previous globals on cleanup.
// Instruments bind to the global meter/tracer at Handler-construction time, so
// callers must install these BEFORE calling backend.NewHandler.
func setupTestProviders(t *testing.T) (*sdkmetric.ManualReader, *tracetest.SpanRecorder) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	prevMP := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	sr := tracetest.NewSpanRecorder()
	prevTP := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))

	t.Cleanup(func() {
		otel.SetMeterProvider(prevMP)
		otel.SetTracerProvider(prevTP)
	})
	return reader, sr
}

func collectMetric(t *testing.T, reader *sdkmetric.ManualReader, name string) *metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}

func attrMapOfSum(t *testing.T, m *metricdata.Metrics) map[string]string {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "metric %s is not an int64 sum", m.Name)
	require.Len(t, sum.DataPoints, 1, "metric %s data points", m.Name)
	got := map[string]string{}
	for _, kv := range sum.DataPoints[0].Attributes.ToSlice() {
		got[string(kv.Key)] = kv.Value.String()
	}
	return got
}

// newAuthoritativeTestConfig builds a minimal in-memory zone db authoritative
// for "example.test." with a single A record, mirroring the trailing-dot
// convention ReadZoneFiles/ApplyDefaults apply to real zone files.
func newAuthoritativeTestConfig() *config.Config {
	cfg := &config.Config{
		Records: make(map[config.DomainLookup][]config.Records),
		Domain:  make(map[string]config.Domain),
	}
	const fqdn = "example.test."
	key := config.DomainLookup{Domain: fqdn, Type: dns.TypeA, Class: dns.ClassINET}
	cfg.Records[key] = []config.Records{{Domain: fqdn, TTL: 60, Type: dns.TypeA, Class: dns.ClassINET, Address: "10.0.0.5"}}
	cfg.Domain["example.test"] = config.Domain{Domain: "example.test", SOA: "ns.example.test.", Active: true}
	return cfg
}

func TestServeDNSRecordsQueryMetricAndSpan(t *testing.T) {
	reader, sr := setupTestProviders(t)

	// Instruments bind to the global meter/tracer at construction, so the
	// Handler must be built after the test providers are installed above.
	handler := backend.NewHandler(newAuthoritativeTestConfig(), backend.NewUpstream(nil))

	m := new(dns.Msg)
	m.SetQuestion("example.test.", dns.TypeA)

	w := &fakeResponseWriter{}
	handler.ServeDNS(w, m)

	require.NotNil(t, w.msg)
	assert.Equal(t, dns.RcodeSuccess, w.msg.Rcode)
	assert.True(t, w.msg.Authoritative)

	queries := collectMetric(t, reader, "northstar.dns.queries")
	require.NotNil(t, queries, "northstar.dns.queries not recorded")
	attrs := attrMapOfSum(t, queries)
	assert.Equal(t, "A", attrs["qtype"])
	assert.Equal(t, "NOERROR", attrs["rcode"])
	assert.Equal(t, "udp", attrs["transport"])
	assert.Equal(t, "success", attrs["outcome"])
	assert.Equal(t, "true", attrs["authoritative"])

	duration := collectMetric(t, reader, "northstar.dns.query.duration")
	require.NotNil(t, duration, "northstar.dns.query.duration not recorded")
	hist, ok := duration.Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	require.Len(t, hist.DataPoints, 1)
	assert.Equal(t, uint64(1), hist.DataPoints[0].Count)

	spans := sr.Ended()
	require.Len(t, spans, 1, "expected exactly one dns.query span")
	assert.Equal(t, "dns.query", spans[0].Name())

	spanAttrs := map[string]string{}
	for _, kv := range spans[0].Attributes() {
		spanAttrs[string(kv.Key)] = kv.Value.String()
	}
	assert.Equal(t, "example.test.", spanAttrs["dns.question.name"])
	assert.Equal(t, "A", spanAttrs["dns.question.type"])
	assert.Equal(t, "IN", spanAttrs["dns.question.class"])
	assert.Equal(t, "udp", spanAttrs["network.transport"])
	assert.Equal(t, "0", spanAttrs["dns.response.rcode"]) // dns.RcodeSuccess == 0
	assert.Equal(t, "true", spanAttrs["dns.authoritative"])
	assert.Equal(t, "1", spanAttrs["dns.answer.count"])
}

// TestServeDNSUpstreamForwardMetricAndSpan drives a non-authoritative query
// through a fake upstream (startFakeUpstream is defined in upstream_test.go,
// same backend_test package) and asserts the upstream forward metrics and
// the dns.upstream.exchange child span parented under dns.query.
func TestServeDNSUpstreamForwardMetricAndSpan(t *testing.T) {
	reader, sr := setupTestProviders(t)

	upstreamAddr := startFakeUpstream(t, "recurse.example", "203.0.113.20")
	upstream := backend.NewUpstream(backend.ParseUpstreamServers([]string{upstreamAddr}))

	cfg := &config.Config{
		Records: make(map[config.DomainLookup][]config.Records),
		Domain:  make(map[string]config.Domain),
	}
	handler := backend.NewHandler(cfg, upstream)

	m := new(dns.Msg)
	m.SetQuestion("recurse.example.", dns.TypeA)

	w := &fakeResponseWriter{}
	handler.ServeDNS(w, m)

	require.NotNil(t, w.msg)
	assert.Equal(t, dns.RcodeSuccess, w.msg.Rcode)
	require.NotEmpty(t, w.msg.Answer)

	forwards := collectMetric(t, reader, "northstar.dns.upstream.forwards")
	require.NotNil(t, forwards, "northstar.dns.upstream.forwards not recorded")
	attrs := attrMapOfSum(t, forwards)
	assert.Equal(t, upstreamAddr, attrs["server"])
	assert.Equal(t, "success", attrs["outcome"])

	require.NotNil(t, collectMetric(t, reader, "northstar.dns.upstream.duration"),
		"northstar.dns.upstream.duration not recorded")

	spans := sr.Ended()
	var rootID, childParent trace.SpanID
	var sawChild bool
	for _, s := range spans {
		switch s.Name() {
		case "dns.query":
			rootID = s.SpanContext().SpanID()
		case "dns.upstream.exchange":
			childParent = s.Parent().SpanID()
			sawChild = true
		}
	}
	require.True(t, sawChild, "expected a dns.upstream.exchange span")
	assert.Equal(t, rootID, childParent, "dns.upstream.exchange should be parented under dns.query")
}
