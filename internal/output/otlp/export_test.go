package otlp

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/colinedwardwood/network-topology-exporter/internal/version"
)

// newTestExporter builds an Exporter wired to in-memory SDK plumbing instead of
// an OTLP transport: a metric ManualReader and an in-memory log exporter. It
// exercises the identical PushGraph/PushChanges code paths and lets tests
// assert the SEMANTIC content (metric names, attributes, log bodies) that the
// SDK would otherwise marshal to the wire.
func newTestExporter(instanceID string) (*Exporter, *sdkmetric.ManualReader, *memLogExporter) {
	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version.Version),
			semconv.ServiceInstanceID(instanceID),
		),
	)
	if err != nil {
		panic(err)
	}
	reader := sdkmetric.NewManualReader()
	logExp := &memLogExporter{}
	exp, err := assemble(res, reader, log.NewSimpleProcessor(logExp))
	if err != nil {
		panic(err)
	}
	return exp, reader, logExp
}

// memLogExporter is an in-memory log.Exporter that retains every record it is
// asked to export so tests can assert on bodies, severities, and attributes.
type memLogExporter struct {
	mu      sync.Mutex
	records []log.Record
}

func (m *memLogExporter) Export(_ context.Context, records []log.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range records {
		m.records = append(m.records, r.Clone())
	}
	return nil
}

func (m *memLogExporter) Shutdown(context.Context) error { return nil }

func (m *memLogExporter) ForceFlush(context.Context) error { return nil }

func (m *memLogExporter) all() []log.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]log.Record, len(m.records))
	copy(out, m.records)
	return out
}
