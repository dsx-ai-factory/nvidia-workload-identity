package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	sdklog "go.opentelemetry.io/otel/sdk/log"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

func TestResourceServiceNamePrecedence(t *testing.T) {
	tests := []struct {
		name               string
		serviceName        string
		resourceAttributes string
		want               string
	}{
		{name: "fallback", want: "fallback"},
		{name: "resource attributes", resourceAttributes: "service.name=resource", want: "resource"},
		{
			name:               "service name",
			serviceName:        "service",
			resourceAttributes: "service.name=resource",
			want:               "service",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("OTEL_SERVICE_NAME", test.serviceName)
			t.Setenv("OTEL_RESOURCE_ATTRIBUTES", test.resourceAttributes)

			res, err := newResource(t.Context(), "fallback")
			if err != nil {
				t.Fatalf("newResource() error = %v", err)
			}
			value, ok := res.Set().Value(semconv.ServiceNameKey)
			if !ok {
				t.Fatal("service.name attribute is missing")
			}
			if got := value.AsString(); got != test.want {
				t.Errorf("service.name = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSetupWithNoExporters(t *testing.T) {
	previousLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")

	var output bytes.Buffer
	sdk, err := Setup(t.Context(), "test-service", &output, slog.LevelInfo)
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	defer func() {
		if err := sdk.Shutdown(t.Context()); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	}()

	if sdk.tracerProvider != nil || sdk.meterProvider != nil || sdk.loggerProvider != nil {
		t.Fatal("Setup() created a provider for a disabled exporter")
	}

	slog.InfoContext(t.Context(), "ready", slog.String("component", "telemetry"))

	if lines := bytes.Count(output.Bytes(), []byte{'\n'}); lines != 1 {
		t.Fatalf("stdout log lines = %d, want 1", lines)
	}
	var record struct {
		Message   string `json:"msg"`
		Component string `json:"component"`
	}
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode stdout log: %v", err)
	}
	if record.Message != "ready" || record.Component != "telemetry" {
		t.Errorf("stdout log = %+v", record)
	}
}

func TestSetupAcceptsCaseInsensitiveSDKDisabled(t *testing.T) {
	previousLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	t.Setenv("OTEL_SDK_DISABLED", "TRUE")
	t.Setenv("OTEL_TRACES_EXPORTER", "invalid")
	t.Setenv("OTEL_METRICS_EXPORTER", "invalid")
	t.Setenv("OTEL_LOGS_EXPORTER", "invalid")

	sdk, err := Setup(t.Context(), "test-service", io.Discard, slog.LevelInfo)
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if sdk.tracerProvider != nil || sdk.meterProvider != nil || sdk.loggerProvider != nil {
		t.Fatal("Setup() created a provider while the SDK was disabled")
	}
}

func TestProtocolOnlyUsesDefaultTraceExporter(t *testing.T) {
	previousLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_TRACES_EXPORTER", "")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "")
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")

	sdk, err := Setup(t.Context(), "test-service", io.Discard, slog.LevelInfo)
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	defer func() {
		if err := sdk.Shutdown(t.Context()); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	}()
	if sdk.tracerProvider == nil {
		t.Error("protocol-only configuration did not use the default OTLP trace exporter")
	}
	if sdk.meterProvider != nil || sdk.loggerProvider != nil {
		t.Error("disabled metric or log exporter created a provider")
	}
}

func TestSlogHandlerUsesStdoutLogLevelForExport(t *testing.T) {
	exporter := new(recordingLogExporter)
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	sdk := &SDK{loggerProvider: provider}
	t.Cleanup(func() {
		if err := sdk.Shutdown(t.Context()); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})

	var output bytes.Buffer
	stdout := slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(sdk.slogHandler("test", stdout))
	logger.DebugContext(t.Context(), "suppressed")
	logger.InfoContext(t.Context(), "exported")

	if len(exporter.records) != 1 {
		t.Fatalf("exported records = %d, want 1", len(exporter.records))
	}
	if got := exporter.records[0].Body().AsString(); got != "exported" {
		t.Errorf("exported message = %q, want exported", got)
	}
	if lines := bytes.Count(output.Bytes(), []byte{'\n'}); lines != 1 {
		t.Errorf("stdout log lines = %d, want 1", lines)
	}
}

type recordingLogExporter struct {
	records []sdklog.Record
}

func (e *recordingLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	for _, record := range records {
		e.records = append(e.records, record.Clone())
	}
	return nil
}

func (*recordingLogExporter) Shutdown(context.Context) error {
	return nil
}

func (*recordingLogExporter) ForceFlush(context.Context) error {
	return nil
}
