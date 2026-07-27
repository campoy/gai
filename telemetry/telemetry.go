// Package telemetry exports OpenTelemetry traces over OTLP/gRPC.
//
// Spans go to a collector on localhost:4317 by default — a Jaeger container or
// any other OTLP receiver.
package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	// serviceName identifies this program in the trace UI.
	serviceName = "gai"

	// localEndpoint is the standard OTLP/gRPC port, where a local collector
	// listens. Start one with:
	//
	//	docker run --rm -p 16686:16686 -p 4317:4317 \
	//		cr.jaegertracing.io/jaegertracing/jaeger:2.20.0
	localEndpoint = "localhost:4317"
)

type config struct {
	endpoint string
}

// An Option configures where traces are sent.
type Option func(*config)

// WithEndpoint sends spans to the given host:port instead of the default local
// collector. The connection is plaintext, so this is for collectors you run
// yourself; a hosted backend would also need TLS and an auth header.
func WithEndpoint(hostport string) Option {
	return func(c *config) { c.endpoint = hostport }
}

// Init installs a global tracer provider and returns a function that flushes
// pending spans. Spans are batched, so the returned function must run before
// the program exits or the trace is lost.
//
// The exporter dials lazily: if no collector is listening the program still
// works, it just drops spans.
func Init(ctx context.Context, opts ...Option) (func(context.Context) error, error) {
	cfg := config{endpoint: localEndpoint}
	for _, opt := range opts {
		opt(&cfg)
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("creating OTLP exporter: %w", err)
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
	))
	if err != nil {
		return nil, fmt.Errorf("building resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

// Tracer returns the tracer every span in this program is created from.
func Tracer() trace.Tracer { return otel.Tracer(serviceName) }
