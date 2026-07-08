package main

import (
	"context"
	"errors"
	"testing"
)

func TestRunWaitsForCancellationAfterSetup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	setupCalled := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- run(ctx, func(context.Context) error {
			close(setupCalled)
			return nil
		})
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
	err := run(context.Background(), func(context.Context) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("run error = %v, want %v", err, want)
	}
}

func TestRunTreatsCancellationDuringSetupAsShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := run(ctx, func(context.Context) error { return context.Canceled }); err != nil {
		t.Fatalf("run error = %v, want nil", err)
	}
}
