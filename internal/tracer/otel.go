package tracer

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	crand "crypto/rand"
	"encoding/binary"
	"math/rand"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const tracerName = "tilt.dev/usage"

// fixedTraceIDGenerator always returns the same TraceID (from TILT_TRACE_ID env)
// so that all tilt-internal spans join the parent trace started by tilt.sh.
type fixedTraceIDGenerator struct {
	traceID trace.TraceID
	mu      sync.Mutex
	rand    *rand.Rand
}

func newFixedTraceIDGenerator(tid trace.TraceID) *fixedTraceIDGenerator {
	var seed int64
	_ = binary.Read(crand.Reader, binary.LittleEndian, &seed)
	return &fixedTraceIDGenerator{
		traceID: tid,
		rand:    rand.New(rand.NewSource(seed)),
	}
}

func (g *fixedTraceIDGenerator) NewIDs(ctx context.Context) (trace.TraceID, trace.SpanID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	sid := trace.SpanID{}
	for {
		_, _ = g.rand.Read(sid[:])
		if sid.IsValid() {
			break
		}
	}
	return g.traceID, sid
}

func (g *fixedTraceIDGenerator) NewSpanID(ctx context.Context, traceID trace.TraceID) trace.SpanID {
	g.mu.Lock()
	defer g.mu.Unlock()
	sid := trace.SpanID{}
	for {
		_, _ = g.rand.Read(sid[:])
		if sid.IsValid() {
			break
		}
	}
	return sid
}

// rootParentTracer wraps a trace.Tracer to automatically inject a remote
// parent span context for top-level spans. This ensures that all tilt-native
// spans (update, image-build, k8s-deploy, etc.) appear as children of the
// tilt.sh root span in the trace viewer.
type rootParentTracer struct {
	embedded.Tracer
	inner  trace.Tracer
	rootSC trace.SpanContext
}

func (t *rootParentTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	// If there's no active parent span in the context, inject the root span
	// as a remote parent so this span becomes a child of tilt.sh.
	if !trace.SpanContextFromContext(ctx).IsValid() {
		ctx = trace.ContextWithRemoteSpanContext(ctx, t.rootSC)
	}
	return t.inner.Start(ctx, name, opts...)
}

func InitOpenTelemetry(exporter sdktrace.SpanExporter) trace.Tracer {
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	}

	// Register the existing exporter (SpanCollector for telemetry controller).
	if exporter != nil {
		sp := sdktrace.NewBatchSpanProcessor(exporter)
		opts = append(opts, sdktrace.WithSpanProcessor(sp))
	}

	// If TILT_OTEL_ENDPOINT is set, also export via OTLP/gRPC.
	if endpoint := os.Getenv("TILT_OTEL_ENDPOINT"); endpoint != "" {
		otlpExporter, err := newOTLPExporter(endpoint)
		if err == nil {
			sp := sdktrace.NewBatchSpanProcessor(otlpExporter)
			opts = append(opts, sdktrace.WithSpanProcessor(sp))
		} else {
			fmt.Fprintf(os.Stderr, "WARNING: failed to create OTLP exporter for %s: %v\n", endpoint, err)
		}
	}

	var traceID trace.TraceID
	var hasTraceID bool

	// If TILT_TRACE_ID is set, pin all spans to that trace so they join
	// the umbrella trace started by tilt.sh.
	if traceIDHex := os.Getenv("TILT_TRACE_ID"); traceIDHex != "" {
		if tid, err := parseTraceID(traceIDHex); err == nil {
			opts = append(opts, sdktrace.WithIDGenerator(newFixedTraceIDGenerator(tid)))
			traceID = tid
			hasTraceID = true
		}
	}

	// Build resource with service name and optional session attributes.
	res := buildResource()
	if res != nil {
		opts = append(opts, sdktrace.WithResource(res))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	tracer := tp.Tracer(tracerName)

	// If TILT_ROOT_SPAN_ID is set, wrap the tracer so that top-level spans
	// (those without a parent in context) become children of the root span.
	if hasTraceID {
		if rootSpanHex := os.Getenv("TILT_ROOT_SPAN_ID"); rootSpanHex != "" {
			if sid, err := parseSpanID(rootSpanHex); err == nil {
				rootSC := trace.NewSpanContext(trace.SpanContextConfig{
					TraceID:    traceID,
					SpanID:     sid,
					TraceFlags: trace.FlagsSampled,
					Remote:     true,
				})
				tracer = &rootParentTracer{inner: tracer, rootSC: rootSC}
			}
		}
	}

	return tracer
}

func newOTLPExporter(endpoint string) (sdktrace.SpanExporter, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		otlptracegrpc.WithTimeout(5*time.Second),
	)
}

func parseTraceID(s string) (trace.TraceID, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return trace.TraceID{}, fmt.Errorf("invalid trace ID %q", s)
	}
	var tid trace.TraceID
	copy(tid[:], b)
	return tid, nil
}

func parseSpanID(s string) (trace.SpanID, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 8 {
		return trace.SpanID{}, fmt.Errorf("invalid span ID %q", s)
	}
	var sid trace.SpanID
	copy(sid[:], b)
	return sid, nil
}

func buildResource() *resource.Resource {
	attrs := []attribute.KeyValue{
		attribute.String("service.name", "tilt"),
	}
	if v := os.Getenv("TILT_SESSION_ID"); v != "" {
		attrs = append(attrs, attribute.String("tilt.session.id", v))
	}
	if v := os.Getenv("TILT_NAMESPACE"); v != "" {
		attrs = append(attrs, attribute.String("tilt.namespace", v))
	}
	if v := os.Getenv("TILT_GIT_AUTHOR"); v != "" {
		attrs = append(attrs, attribute.String("tilt.git.author", v))
	}
	if v := os.Getenv("TILT_REQUESTEE"); v != "" {
		attrs = append(attrs, attribute.String("tilt.requestee", v))
	}
	r, err := resource.New(context.Background(),
		resource.WithAttributes(attrs...),
	)
	if err != nil {
		return nil
	}
	return r
}
