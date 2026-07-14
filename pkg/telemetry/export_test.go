package telemetry

import (
	"context"
	"sync"

	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// recordingLogExporter is a minimal sdk/log.Exporter that records every
// exported record for assertions, used since v0.20.0 ships no built-in
// in-memory test exporter for external consumers.
type recordingLogExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

var _ sdklog.Exporter = (*recordingLogExporter)(nil)

func (e *recordingLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range records {
		e.records = append(e.records, r.Clone())
	}
	return nil
}

func (e *recordingLogExporter) Shutdown(context.Context) error   { return nil }
func (e *recordingLogExporter) ForceFlush(context.Context) error { return nil }

func (e *recordingLogExporter) snapshot() []sdklog.Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]sdklog.Record(nil), e.records...)
}
