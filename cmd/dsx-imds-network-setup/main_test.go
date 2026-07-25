package main

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type fakeNetworkSetup struct {
	called chan struct{}
	err    error
}

func (f fakeNetworkSetup) Setup(context.Context) error {
	if f.called != nil {
		close(f.called)
	}
	return f.err
}

func TestRunWaitsForCancellationAfterSetup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	setupCalled := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- run(ctx, fakeNetworkSetup{called: setupCalled})
	}()

	<-setupCalled
	select {
	case err := <-done:
		t.Fatalf("run returned before cancellation: %v", err)
	default:
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run returned an error after cancellation: %v", err)
	}
}

func TestRunReturnsSetupError(t *testing.T) {
	want := errors.New("setup failed")
	err := run(context.Background(), fakeNetworkSetup{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("run error = %v, want %v", err, want)
	}
}

func TestRunTreatsCancellationDuringSetupAsShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := run(ctx, fakeNetworkSetup{err: context.Canceled}); err != nil {
		t.Fatalf("run error = %v, want nil", err)
	}
}

func TestRunTracesNetworkSetup(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("shut down trace provider: %v", err)
		}
	})

	want := errors.New("setup failed")
	if err := run(context.Background(), fakeNetworkSetup{err: want}); !errors.Is(err, want) {
		t.Fatalf("run error = %v, want %v", err, want)
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if got := spans[0].Name(); got != "configure-pod-network" {
		t.Errorf("span name = %q, want configure-pod-network", got)
	}
	if got := spans[0].Status().Code; got != codes.Error {
		t.Errorf("span status = %v, want error", got)
	}
}
