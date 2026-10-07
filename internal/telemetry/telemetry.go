// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	runtimeotel "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/contrib/propagators/autoprop"
	"go.opentelemetry.io/otel"
	otellogglobal "go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// SDK owns the OpenTelemetry providers configured for a process.
type SDK struct {
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
	loggerProvider *sdklog.LoggerProvider
}

// Setup configures JSON slog output and OpenTelemetry from standard environment variables.
func Setup(ctx context.Context, fallbackServiceName string, output io.Writer, level slog.Leveler) (*SDK, error) {
	consoleHandler := slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(consoleHandler))

	// Keep SDK diagnostics out of the configured log exporter so an export
	// failure cannot recursively produce another exported log record.
	otelErrorLogger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		otelErrorLogger.Error("OpenTelemetry error", slog.Any("error", err))
	}))

	sdk := new(SDK)
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		otel.SetTextMapPropagator(autoprop.NewTextMapPropagator())
		return sdk, nil
	}

	res, err := newResource(ctx, fallbackServiceName)
	if err != nil {
		return nil, fmt.Errorf("configure OpenTelemetry resource: %w", err)
	}

	exporter, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return setupFailure(ctx, sdk, fmt.Errorf("configure trace exporter: %w", err), exporter)
	}
	if autoexport.IsNoneSpanExporter(exporter) {
		if err := exporter.Shutdown(ctx); err != nil {
			return setupFailure(ctx, sdk, fmt.Errorf("shut down disabled trace exporter: %w", err), nil)
		}
	} else {
		sdk.tracerProvider = sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithBatcher(exporter),
		)
	}

	reader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return setupFailure(ctx, sdk, fmt.Errorf("configure metric reader: %w", err), reader)
	}
	if autoexport.IsNoneMetricReader(reader) {
		if err := reader.Shutdown(ctx); err != nil {
			return setupFailure(ctx, sdk, fmt.Errorf("shut down disabled metric reader: %w", err), nil)
		}
	} else {
		sdk.meterProvider = sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(reader),
		)
	}

	logExporter, err := autoexport.NewLogExporter(ctx)
	if err != nil {
		return setupFailure(ctx, sdk, fmt.Errorf("configure log exporter: %w", err), logExporter)
	}
	if autoexport.IsNoneLogExporter(logExporter) {
		if err := logExporter.Shutdown(ctx); err != nil {
			return setupFailure(ctx, sdk, fmt.Errorf("shut down disabled log exporter: %w", err), nil)
		}
	} else {
		sdk.loggerProvider = sdklog.NewLoggerProvider(
			sdklog.WithResource(res),
			sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
		)
	}

	if sdk.meterProvider != nil {
		if err := runtimeotel.Start(runtimeotel.WithMeterProvider(sdk.meterProvider)); err != nil {
			return setupFailure(ctx, sdk, fmt.Errorf("start Go runtime metrics: %w", err), nil)
		}
	}

	otel.SetTextMapPropagator(autoprop.NewTextMapPropagator())
	if sdk.tracerProvider != nil {
		otel.SetTracerProvider(sdk.tracerProvider)
	}
	if sdk.meterProvider != nil {
		otel.SetMeterProvider(sdk.meterProvider)
	}
	if sdk.loggerProvider != nil {
		otellogglobal.SetLoggerProvider(sdk.loggerProvider)
		slog.SetDefault(slog.New(sdk.slogHandler(fallbackServiceName, consoleHandler)))
	}

	return sdk, nil
}

func (sdk *SDK) slogHandler(name string, console slog.Handler) slog.Handler {
	if sdk == nil || sdk.loggerProvider == nil {
		return console
	}
	return filteredHandler{
		handler: slog.NewMultiHandler(
			console,
			otelslog.NewHandler(name, otelslog.WithLoggerProvider(sdk.loggerProvider)),
		),
		enabled: console,
	}
}

// Shutdown flushes and stops every configured provider.
func (sdk *SDK) Shutdown(ctx context.Context) error {
	if sdk == nil {
		return nil
	}
	var err error
	if sdk.loggerProvider != nil {
		err = errors.Join(err, sdk.loggerProvider.Shutdown(ctx))
	}
	if sdk.meterProvider != nil {
		err = errors.Join(err, sdk.meterProvider.Shutdown(ctx))
	}
	if sdk.tracerProvider != nil {
		err = errors.Join(err, sdk.tracerProvider.Shutdown(ctx))
	}
	return err
}

func newResource(ctx context.Context, fallbackServiceName string) (*resource.Resource, error) {
	return resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(semconv.ServiceName(fallbackServiceName)),
		resource.WithFromEnv(),
	)
}

type shutdowner interface {
	Shutdown(context.Context) error
}

type filteredHandler struct {
	handler slog.Handler
	enabled slog.Handler
}

func (h filteredHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.enabled.Enabled(ctx, level)
}

func (h filteredHandler) Handle(ctx context.Context, record slog.Record) error {
	if !h.Enabled(ctx, record.Level) {
		return nil
	}
	return h.handler.Handle(ctx, record)
}

func (h filteredHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return filteredHandler{
		handler: h.handler.WithAttrs(attrs),
		enabled: h.enabled.WithAttrs(attrs),
	}
}

func (h filteredHandler) WithGroup(name string) slog.Handler {
	return filteredHandler{
		handler: h.handler.WithGroup(name),
		enabled: h.enabled.WithGroup(name),
	}
}

func setupFailure(ctx context.Context, sdk *SDK, err error, partial shutdowner) (*SDK, error) {
	if partial != nil {
		err = errors.Join(err, partial.Shutdown(ctx))
	}
	return nil, errors.Join(err, sdk.Shutdown(ctx))
}
