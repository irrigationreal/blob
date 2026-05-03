// OTel + Prometheus instrumentation for blobd.
//
// Metrics are always exported (zero-cost when nothing scrapes /metrics).
// Tracing is OPT-IN — blobd looks at OTEL_EXPORTER_OTLP_ENDPOINT or, if
// the operator has registered a Tempo via `blob tempo create`, the first
// such instance. If neither is set, tracing is a no-op.
//
// We instrument three high-level paths via TraceAction: deployments,
// scale, and restart. Add more in handlers via TraceAction(ctx,
// "deploy.app", attrs...).
package server

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

var (
	deployTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "blob_deploy_total",
		Help: "Total number of deploys handled by blobd, partitioned by form and outcome.",
	}, []string{"form", "outcome"})
	deployDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "blob_deploy_duration_seconds",
		Help:    "Time spent in handleDeploy/handleDeployImage end-to-end.",
		Buckets: prometheus.ExponentialBuckets(0.1, 2, 10),
	}, []string{"form"})
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "blob_http_requests_total",
		Help: "HTTP requests served by the blobd control plane.",
	}, []string{"method", "path", "status"})
)

var (
	tracerOnce sync.Once
	tracer     trace.Tracer
)

// initTracing configures the global tracer to export to the first available
// OTLP gRPC endpoint. Picks endpoint in this order: explicit env
// OTEL_EXPORTER_OTLP_ENDPOINT, then the first Tempo registered with blobd.
// Errors are logged but don't fail blobd startup — tracing is optional.
//
// Safe to call multiple times; only the first call has effect.
func (s *Server) initTracing() {
	tracerOnce.Do(func() {
		endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
		if endpoint == "" {
			endpoint = s.firstTempoOTLP()
		}
		if endpoint == "" {
			tracer = otel.Tracer("blobd") // no-op when no provider set
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		exp, err := otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(endpoint),
			otlptracegrpc.WithInsecure(),
		)
		if err != nil {
			stdLog("tracing: otlp dial %s failed: %v (tracing disabled)", endpoint, err)
			tracer = otel.Tracer("blobd")
			return
		}
		res, _ := sdkresource.New(ctx,
			sdkresource.WithAttributes(
				semconv.ServiceName("blobd"),
				semconv.ServiceVersion(os.Getenv("BLOB_VERSION")),
			),
		)
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exp),
			sdktrace.WithResource(res),
		)
		otel.SetTracerProvider(tp)
		tracer = tp.Tracer("blobd")
		stdLog("tracing: exporting OTLP traces to %s", endpoint)
	})
}

// TraceAction wraps a unit of work in a span. Returns ctx with the span
// attached and a finish func. If tracing is disabled the span is a no-op.
func (s *Server) TraceAction(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, func(error)) {
	if tracer == nil {
		s.initTracing()
	}
	ctx, span := tracer.Start(ctx, name, trace.WithAttributes(attrs...))
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
		}
		span.End()
	}
}
