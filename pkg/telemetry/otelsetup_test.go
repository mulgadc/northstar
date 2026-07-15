package telemetry

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace/noop"

	loggerglobal "go.opentelemetry.io/otel/log/global"
	lognoop "go.opentelemetry.io/otel/log/noop"
)

func TestInitWithoutEndpointIsNoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "")
	// Pin a known no-op provider: the otel global delegator cannot be reset
	// once another test has installed a real provider.
	otel.SetTracerProvider(noop.NewTracerProvider())

	shutdown, err := Init(context.Background(), "test-svc")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	_, span := otel.Tracer("test").Start(context.Background(), "op")
	defer span.End()
	if span.SpanContext().IsValid() {
		t.Error("expected no-op tracer without endpoint, got recording span")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

func TestInitWithEndpointInstallsProviders(t *testing.T) {
	// Point at a dead endpoint: exporters dial lazily, so Init must still
	// succeed and install real (recording) providers.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	prevTracer := otel.GetTracerProvider()
	prevMeter := otel.GetMeterProvider()
	defer otel.SetTracerProvider(prevTracer)
	defer otel.SetMeterProvider(prevMeter)

	shutdown, err := Init(context.Background(), "test-svc")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	_, span := otel.Tracer("test").Start(context.Background(), "op")
	if !span.SpanContext().IsValid() {
		t.Error("expected recording tracer with endpoint set, got no-op span")
	}
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// Flush hits the dead endpoint; only assert it returns rather than hangs.
	_ = shutdown(ctx)
}

func TestExportEnabled(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"nothing set", nil, false},
		{"endpoint set", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317"}, true},
		{"traces endpoint only", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://localhost:4317"}, true},
		{"disabled overrides endpoint", map[string]string{
			"OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317",
			"OTEL_SDK_DISABLED":           "true",
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{
				"OTEL_EXPORTER_OTLP_ENDPOINT",
				"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
				"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
				"OTEL_SDK_DISABLED",
			} {
				t.Setenv(key, tt.env[key])
			}
			if got := exportEnabled(); got != tt.want {
				t.Errorf("exportEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewResourceAttributes(t *testing.T) {
	t.Setenv("MULGA_ENV", "env19")
	t.Setenv("MULGA_SOURCE", "ci")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "ci.run_id=12345")

	res, err := newResource(context.Background(), "test-svc")
	if err != nil {
		t.Fatalf("newResource: %v", err)
	}
	got := map[string]string{}
	for _, kv := range res.Attributes() {
		got[string(kv.Key)] = kv.Value.String()
	}
	for key, want := range map[string]string{
		"service.name": "test-svc",
		"mulga.env":    "env19",
		"mulga.source": "ci",
		"ci.run_id":    "12345",
	} {
		if got[key] != want {
			t.Errorf("resource attr %s = %q, want %q", key, got[key], want)
		}
	}
	if got["host.name"] == "" {
		t.Error("resource attr host.name missing")
	}
}

// TestInitWithoutEndpointInstallsNoLoggerProvider is the prod-safety
// guarantee: with no OTLP endpoint configured, Init must not install a
// LoggerProvider — the global stays whatever was there before.
func TestInitWithoutEndpointInstallsNoLoggerProvider(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "")

	prevLP := loggerglobal.GetLoggerProvider()
	defer loggerglobal.SetLoggerProvider(prevLP)
	sentinelLP := lognoop.NewLoggerProvider()
	loggerglobal.SetLoggerProvider(sentinelLP)

	shutdown, err := Init(context.Background(), "test-svc")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	if _, ok := loggerglobal.GetLoggerProvider().(lognoop.LoggerProvider); !ok {
		t.Errorf("expected global LoggerProvider to remain the noop sentinel, got %T", loggerglobal.GetLoggerProvider())
	}
}

// TestInitWithEndpointInstallsLoggerProvider proves the same export gate
// installs a real sdk/log LoggerProvider once an OTLP endpoint is configured.
func TestInitWithEndpointInstallsLoggerProvider(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	prevLP := loggerglobal.GetLoggerProvider()
	defer loggerglobal.SetLoggerProvider(prevLP)
	prevTracer := otel.GetTracerProvider()
	prevMeter := otel.GetMeterProvider()
	defer otel.SetTracerProvider(prevTracer)
	defer otel.SetMeterProvider(prevMeter)

	shutdown, err := Init(context.Background(), "test-svc")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	if _, ok := loggerglobal.GetLoggerProvider().(*sdklog.LoggerProvider); !ok {
		t.Errorf("expected a real sdk/log LoggerProvider, got %T", loggerglobal.GetLoggerProvider())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = shutdown(ctx)
}
