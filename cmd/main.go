// Command toggle-web-baker is the FrontendApp deploy-platform operator.
package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
	"github.com/toggle-corp/toggle-web-baker/internal/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = bakerv1alpha1.AddToScheme(scheme)
}

func main() {
	// All operator config now arrives via a single mounted YAML file (the
	// Helm-rendered ConfigMap), replacing the former ~17 CLI flags. Only the
	// controller-runtime zap logging flags and the config path remain flags.
	var configPath string
	flag.StringVar(&configPath, "config", "/etc/baker/config.yaml", "path to the operator config file (Helm-rendered ConfigMap)")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	cfg, mgrOpts, err := controller.LoadConfig(configPath)
	if err != nil {
		setupLog.Error(err, "invalid operator config", "path", configPath)
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: mgrOpts.MetricsBindAddress},
		HealthProbeBindAddress: mgrOpts.HealthProbeBindAddress,
		LeaderElection:         mgrOpts.LeaderElect,
		LeaderElectionID:       "frontendapp.baker.toggle-corp.com",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	r := &controller.FrontendAppReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Config:           cfg,
		StorageClassName: mgrOpts.StorageClass,
		TraefikNamespace: mgrOpts.TraefikNamespace,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "FrontendApp")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
