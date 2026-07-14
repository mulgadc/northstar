package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"

	loggerglobal "go.opentelemetry.io/otel/log/global"
	lognoop "go.opentelemetry.io/otel/log/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

func testSpanContext() trace.SpanContext {
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		SpanID:  trace.SpanID{0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x01, 0x02},
	})
}

func TestSlogHandlerStampsTraceIDs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewSlogHandler(slog.NewJSONHandler(&buf, nil)))

	sc := testSpanContext()
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.InfoContext(ctx, "with span")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("unmarshal log line: %v", err)
	}
	if line["trace_id"] != sc.TraceID().String() {
		t.Errorf("trace_id = %v, want %s", line["trace_id"], sc.TraceID())
	}
	if line["span_id"] != sc.SpanID().String() {
		t.Errorf("span_id = %v, want %s", line["span_id"], sc.SpanID())
	}
}

func TestSlogHandlerNoSpanNoStamp(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewSlogHandler(slog.NewJSONHandler(&buf, nil)))

	logger.InfoContext(context.Background(), "no span")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("unmarshal log line: %v", err)
	}
	if _, ok := line["trace_id"]; ok {
		t.Error("trace_id present on record without span")
	}
	if _, ok := line["span_id"]; ok {
		t.Error("span_id present on record without span")
	}
}

// TestSetDefaultJSONLoggerGatesBridgeAtLevel proves the OTLP bridge is gated
// at the configured level: with level=Info a Debug record must NOT reach the
// bridge while an Info record must, so Debug never floods ES even though the
// bridge's own processor has no severity filter.
func TestSetDefaultJSONLoggerGatesBridgeAtLevel(t *testing.T) {
	exp := &recordingLogExporter{}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)),
	)
	defer func() { _ = lp.Shutdown(context.Background()) }()

	prevLP := loggerglobal.GetLoggerProvider()
	defer loggerglobal.SetLoggerProvider(prevLP)
	loggerglobal.SetLoggerProvider(lp)

	prevHandler := slog.Default().Handler()
	defer slog.SetDefault(slog.New(prevHandler))

	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	SetDefaultJSONLogger(level)

	slog.Debug("debug record should be filtered")
	slog.Info("info record should reach bridge")

	records := exp.snapshot()
	if len(records) != 1 {
		t.Fatalf("got %d exported records, want 1 (only Info+ reaches the gated bridge)", len(records))
	}
	if got := records[0].Body().AsString(); got != "info record should reach bridge" {
		t.Errorf("bridge exported wrong record: body = %q", got)
	}
}

func TestSlogHandlerPreservesWrapperThroughWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewSlogHandler(slog.NewJSONHandler(&buf, nil))).
		With("component", "test").WithGroup("grp")

	sc := testSpanContext()
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.InfoContext(ctx, "wrapped", "k", "v")

	if !bytes.Contains(buf.Bytes(), []byte(sc.TraceID().String())) {
		t.Errorf("trace_id lost after With/WithGroup: %s", buf.String())
	}
}

// TestSetDefaultJSONLoggerNoLoggerProviderIsStdoutOnly is the prod-safety
// guarantee: with no real LoggerProvider installed (the no-op left in place
// by Init), SetDefaultJSONLogger must produce a stdout-only default and
// never wrap it in a fanoutHandler.
func TestSetDefaultJSONLoggerNoLoggerProviderIsStdoutOnly(t *testing.T) {
	prevLP := loggerglobal.GetLoggerProvider()
	defer loggerglobal.SetLoggerProvider(prevLP)
	loggerglobal.SetLoggerProvider(lognoop.NewLoggerProvider())

	prevHandler := slog.Default().Handler()
	defer slog.SetDefault(slog.New(prevHandler))

	SetDefaultJSONLogger(slog.LevelInfo)

	if _, ok := slog.Default().Handler().(*fanoutHandler); ok {
		t.Error("expected stdout-only handler without a real LoggerProvider, got fanoutHandler")
	}
}

// TestSetDefaultJSONLoggerBridgesWithoutClobbering exercises the exact
// wiring Init installs (resource -> LoggerProvider -> otelslog bridge) via a
// recording exporter standing in for the OTLP gRPC exporter. It proves
// exported records carry the full resource (incl. ci.run_id from
// OTEL_RESOURCE_ATTRIBUTES) and the logging context's trace_id, that stdout
// output is preserved alongside the bridge, and that a second
// SetDefaultJSONLogger call still exports rather than clobbering the bridge.
func TestSetDefaultJSONLoggerBridgesWithoutClobbering(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "ci.run_id=TESTRUN")

	res, err := newResource(context.Background(), "northstar")
	if err != nil {
		t.Fatalf("newResource: %v", err)
	}

	exp := &recordingLogExporter{}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)),
	)
	defer func() { _ = lp.Shutdown(context.Background()) }()

	prevLP := loggerglobal.GetLoggerProvider()
	defer loggerglobal.SetLoggerProvider(prevLP)
	loggerglobal.SetLoggerProvider(lp)

	prevHandler := slog.Default().Handler()
	defer slog.SetDefault(slog.New(prevHandler))

	SetDefaultJSONLogger(slog.LevelInfo)
	// A *fanoutHandler default proves both the stdout/journald handler (see
	// TestFanoutHandlerWritesToAllHandlers for its per-handler behavior) and
	// the OTLP bridge are wired in together, not one replacing the other.
	if _, ok := slog.Default().Handler().(*fanoutHandler); !ok {
		t.Fatalf("expected fanoutHandler once a real LoggerProvider is installed, got %T", slog.Default().Handler())
	}

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		SpanID:     trace.SpanID{0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x01, 0x02},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	slog.InfoContext(ctx, "first record")

	// Simulate a repeated SetDefaultJSONLogger call: the bridge must survive
	// rather than being clobbered by the second install.
	SetDefaultJSONLogger(slog.LevelInfo)
	if _, ok := slog.Default().Handler().(*fanoutHandler); !ok {
		t.Fatalf("expected fanoutHandler to survive a second SetDefaultJSONLogger call, got %T", slog.Default().Handler())
	}
	slog.InfoContext(ctx, "second record")

	records := exp.snapshot()
	if len(records) != 2 {
		t.Fatalf("got %d exported records, want 2 (bridge should survive repeated SetDefaultJSONLogger calls)", len(records))
	}
	for i, rec := range records {
		if rec.TraceID() != sc.TraceID() {
			t.Errorf("record %d TraceID = %s, want %s", i, rec.TraceID(), sc.TraceID())
		}
		attrs := map[string]string{}
		for _, kv := range rec.Resource().Attributes() {
			attrs[string(kv.Key)] = kv.Value.Emit()
		}
		if attrs["service.name"] != "northstar" {
			t.Errorf("record %d resource service.name = %q, want northstar", i, attrs["service.name"])
		}
		if attrs["ci.run_id"] != "TESTRUN" {
			t.Errorf("record %d resource ci.run_id = %q, want TESTRUN", i, attrs["ci.run_id"])
		}
	}
}

// recordingSlogHandler is a bare slog.Handler that records whether Handle
// was called, standing in for the pre-existing stdout/journald handler in
// fan-out tests.
type recordingSlogHandler struct {
	mu     sync.Mutex
	called int
}

var _ slog.Handler = (*recordingSlogHandler)(nil)

func (h *recordingSlogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingSlogHandler) Handle(context.Context, slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.called++
	return nil
}

func (h *recordingSlogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingSlogHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingSlogHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.called
}

// TestFanoutHandlerWritesToAllHandlers proves the existing stdout/journald
// handler still fires when the OTLP bridge handler is fanned in alongside it.
func TestFanoutHandlerWritesToAllHandlers(t *testing.T) {
	stdout := &recordingSlogHandler{}
	bridge := &recordingSlogHandler{}
	logger := slog.New(newFanoutHandler(stdout, bridge))

	logger.Info("dual write")

	if stdout.count() != 1 {
		t.Errorf("stdout handler called %d times, want 1", stdout.count())
	}
	if bridge.count() != 1 {
		t.Errorf("bridge handler called %d times, want 1", bridge.count())
	}
}
