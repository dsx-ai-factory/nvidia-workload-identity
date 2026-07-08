package main

import (
	"flag"
	"os"

	spirev1alpha1 "github.com/spiffe/spire-controller-manager/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"dsx-identity/internal/identitycontroller"
)

func main() {
	var config identitycontroller.Config
	var metricsAddress string
	var probeAddress string
	var leaderElection bool

	flag.StringVar(&config.TrustDomain, "trust-domain", "", "local SPIRE trust domain")
	flag.StringVar(&config.OperatorTenant, "operator-tenant", "", "tenant used for DPU identities")
	flag.StringVar(&config.ClassName, "class-name", "", "SPIRE controller manager class name")
	flag.StringVar(&config.DPUHolderNamespace, "dpu-holder-namespace", "", "DPU token holder namespace")
	flag.StringVar(&config.DPUHolderServiceAcct, "dpu-holder-service-account", "", "DPU token holder service account")
	flag.StringVar(&metricsAddress, "metrics-bind-address", ":8080", "metrics listen address")
	flag.StringVar(&probeAddress, "health-probe-bind-address", ":8081", "health probe listen address")
	flag.BoolVar(&leaderElection, "leader-elect", true, "enable leader election")
	flag.Parse()

	ctrl.SetLogger(zap.New())
	log := ctrl.Log.WithName("setup")
	if err := config.Validate(); err != nil {
		log.Error(err, "invalid configuration")
		os.Exit(2)
	}

	scheme := runtime.NewScheme()
	if err := spirev1alpha1.AddToScheme(scheme); err != nil {
		log.Error(err, "register API with scheme")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddress},
		HealthProbeBindAddress: probeAddress,
		LeaderElection:         leaderElection,
		LeaderElectionID:       "dsx-identity-controller.identity.dsx.nvidia.com",
	})
	if err != nil {
		log.Error(err, "create manager")
		os.Exit(1)
	}

	recorder := mgr.GetEventRecorderFor("dsx-identity-controller")
	if err := (&identitycontroller.DPUReconciler{Client: mgr.GetClient(), Config: config, Recorder: recorder}).SetupWithManager(mgr); err != nil {
		log.Error(err, "set up DPU controller")
		os.Exit(1)
	}
	if err := (&identitycontroller.LaunchLayerReconciler{Client: mgr.GetClient(), Config: config, Recorder: recorder}).SetupWithManager(mgr); err != nil {
		log.Error(err, "set up LaunchLayer identity controller")
		os.Exit(1)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "register health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "register readiness check")
		os.Exit(1)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "run manager")
		os.Exit(1)
	}
}
