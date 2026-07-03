// Command toggle-web-baker is the FrontendApp deploy-platform operator.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	zaplib "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
	"github.com/toggle-corp/toggle-web-baker/internal/controller"
	"github.com/toggle-corp/toggle-web-baker/internal/observability"
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

	// Sentry must exist before the logger: the zap core below tees into it.
	// A nil reporter (SENTRY_DSN unset) is the fully disabled no-op mode.
	reporter, err := observability.InitFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to init sentry: %v\n", err)
		os.Exit(1)
	}

	opts.ZapOpts = append(opts.ZapOpts, zaplib.WrapCore(func(c zapcore.Core) zapcore.Core {
		return zapcore.NewTee(c, observability.NewZapCore(reporter))
	}))
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if err := run(configPath, reporter); err != nil {
		os.Exit(1)
	}
}

// run holds the operator lifecycle so every exit path (including errors)
// unwinds through the deferred Sentry flush; main only calls os.Exit after
// run returns. Errors are logged at their site, so callers just exit.
func run(configPath string, reporter *observability.Reporter) error {
	defer reporter.Flush(2 * time.Second)

	cfg, mgrOpts, err := controller.LoadConfig(configPath)
	if err != nil {
		setupLog.Error(err, "invalid operator config", "path", configPath)
		return err
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
		return err
	}

	r := &controller.FrontendAppReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Config:           cfg,
		StorageClassName: mgrOpts.StorageClass,
		TraefikNamespace: mgrOpts.TraefikNamespace,
		Sentry:           reporter,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "FrontendApp")
		return err
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		return err
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		return err
	}
	return nil
}
