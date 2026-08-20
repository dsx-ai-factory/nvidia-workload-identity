package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/go-logr/logr"
	spirev1alpha1 "github.com/spiffe/spire-controller-manager/api/v1alpha1"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"dsx-identity/internal/identitycontroller"
	"dsx-identity/internal/telemetry"
)

func run() (runErr error) {
	var config identitycontroller.Config
	var metricsAddress string
	var probeAddress string
	var leaderElection bool
	var logLevel slog.Level

	flag.StringVar(&config.TrustDomain, "trust-domain", "", "local SPIRE trust domain")
	flag.StringVar(&config.Service, "service", "", "service for controller-managed identities")
	flag.StringVar(&config.OperatorTenant, "operator-tenant", "", "configured tenant for operator identities")
	flag.StringVar(&config.ClassName, "class-name", "", "SPIRE controller manager class name")
	flag.Func("dpu-metadata-selector", "SPIRE selector for DPU-side metadata-service, repeatable", func(value string) error {
		config.DPUMetadataSelectors = append(config.DPUMetadataSelectors, value)
		return nil
	})
	flag.StringVar(&metricsAddress, "metrics-bind-address", ":8080", "metrics listen address")
	flag.StringVar(&probeAddress, "health-probe-bind-address", ":8081", "health probe listen address")
	flag.BoolVar(&leaderElection, "leader-elect", true, "enable leader election")
	flag.TextVar(&logLevel, "log-level", slog.LevelInfo, "log level (debug, info, warn, error)")
	flag.Parse()

	sdk, err := telemetry.Setup(context.Background(), "dsx-identity-controller", os.Stdout, logLevel)
	if err != nil {
		return fmt.Errorf("configure OpenTelemetry: %w", err)
	}
	defer func() {
		runErr = errors.Join(runErr, sdk.Shutdown(context.Background()))
	}()
	ctrl.SetLogger(logr.FromSlogHandler(slog.Default().Handler()))
	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	scheme := runtime.NewScheme()
	if err := spirev1alpha1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("register API with scheme: %w", err)
	}
	restConfig, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("get Kubernetes client configuration: %w", err)
	}
	restConfig.Wrap(func(transport http.RoundTripper) http.RoundTripper {
		return otelhttp.NewTransport(transport)
	})

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddress},
		HealthProbeBindAddress: probeAddress,
		LeaderElection:         leaderElection,
		LeaderElectionID:       "dsx-identity-controller.identity.dsx.nvidia.com",
	})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}

	recorder := mgr.GetEventRecorderFor("dsx-identity-controller")
	if err := (&identitycontroller.DPUReconciler{Client: mgr.GetClient(), Config: config, Recorder: recorder}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up DPU controller: %w", err)
	}
	for _, sourceGVK := range []schema.GroupVersionKind{
		identitycontroller.VirtualInstanceGVK,
		identitycontroller.BareMetalInstanceGVK,
	} {
		if err := (&identitycontroller.LaunchLayerReconciler{
			Client: mgr.GetClient(), Config: config, Recorder: recorder, SourceGVK: sourceGVK,
		}).SetupWithManager(mgr); err != nil {
			return fmt.Errorf("set up %s identity controller: %w", sourceGVK.Kind, err)
		}
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("register health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("register readiness check: %w", err)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("run manager: %w", err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("Identity controller failed", "error", err)
		os.Exit(1)
	}
}
