// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
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

func TestOTLPHTTPTraceExport(t *testing.T) {
	previousLogger := slog.Default()
	previousTracerProvider := otel.GetTracerProvider()
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
		otel.SetTracerProvider(previousTracerProvider)
	})

	requests := make(chan struct{}, 1)
	receiver := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if contentType := r.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/x-protobuf") {
			http.Error(w, "unexpected content type", http.StatusUnsupportedMediaType)
			return
		}
		if r.ContentLength == 0 {
			http.Error(w, "empty trace export", http.StatusBadRequest)
			return
		}
		select {
		case requests <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for OTLP HTTP: %v", err)
	}
	receiver.Listener = listener
	receiver.Start()
	defer receiver.Close()

	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", receiver.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")

	sdk, err := Setup(t.Context(), "trace-export-test", io.Discard, slog.LevelInfo)
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	_, span := otel.Tracer("telemetry-test").Start(t.Context(), "exported-span")
	span.End()

	shutdownCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := sdk.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	select {
	case <-requests:
	case <-shutdownCtx.Done():
		t.Fatal("OTLP HTTP receiver did not receive the exported span")
	}
}

func TestPrometheusMetricCollection(t *testing.T) {
	previousLogger := slog.Default()
	previousMeterProvider := otel.GetMeterProvider()
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
		otel.SetMeterProvider(previousMeterProvider)
	})

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve Prometheus port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release Prometheus port: %v", err)
	}

	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "prometheus")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")
	t.Setenv("OTEL_EXPORTER_PROMETHEUS_HOST", "127.0.0.1")
	t.Setenv("OTEL_EXPORTER_PROMETHEUS_PORT", fmt.Sprintf("%d", port))
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")

	sdk, err := Setup(t.Context(), "metric-collection-test", io.Discard, slog.LevelInfo)
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	defer func() {
		if err := sdk.Shutdown(t.Context()); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	}()

	collectionCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/metrics", port)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		req, err := http.NewRequestWithContext(collectionCtx, http.MethodGet, endpoint, nil)
		if err != nil {
			t.Fatalf("create Prometheus request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				t.Fatalf("read Prometheus response: %v", readErr)
			}
			if resp.StatusCode == http.StatusOK && bytes.Contains(body, []byte("# HELP")) {
				return
			}
		}
		select {
		case <-collectionCtx.Done():
			t.Fatal("Prometheus endpoint did not return collected metrics")
		case <-ticker.C:
		}
	}
}

func TestSlogHandlerUsesStdoutLogLevelForExport(t *testing.T) {
	exporter := new(recordingLogExporter)
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	sdk := &SDK{loggerProvider: provider}
	defer func() {
		if err := sdk.Shutdown(t.Context()); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	}()

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
