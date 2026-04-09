// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package gpumetrics

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"google.golang.org/grpc/credentials"
)

// OTLPStandaloneConfig configures an OTLP gRPC metrics exporter for the standalone
// agent binary (not the OpenTelemetry Collector receiver).
type OTLPStandaloneConfig struct {
	Endpoint   string
	DisableTLS bool
	Interval   time.Duration
	Name       string
	Version    string
}

// NewStandaloneMeterProvider builds an SDK MeterProvider that exports metrics to
// the same OTLP gRPC endpoint style as the profiler reporter (host:port).
func NewStandaloneMeterProvider(ctx context.Context, c OTLPStandaloneConfig) (metric.MeterProvider, func(context.Context) error, error) {
	if c.Endpoint == "" {
		return nil, nil, fmt.Errorf("standalone metrics: empty OTLP endpoint")
	}
	opts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(c.Endpoint),
	}
	if c.DisableTLS {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	} else {
		opts = append(opts, otlpmetricgrpc.WithTLSCredentials(credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13,
		})))
	}
	exporter, err := otlpmetricgrpc.New(ctx, opts...)
	if err != nil {
		return nil, nil, err
	}
	interval := c.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	reader := sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(interval))
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(c.Name),
			semconv.ServiceVersion(c.Version),
		),
	)
	if err != nil {
		return nil, nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	)
	return mp, mp.Shutdown, nil
}
