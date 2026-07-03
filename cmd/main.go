// Command toggle-web-baker is the App deploy-platform operator.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	zaplib "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	// A malformed DSN must NOT crash-loop the operator over optional
	// telemetry: complain loudly and run with Sentry disabled.
	reporter, err := observability.InitFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: unable to init sentry, continuing with Sentry DISABLED: %v\n", err)
		reporter = nil
	}

	// Only pay the tee's per-log dispatch when Sentry is actually on.
	if reporter != nil {
		opts.ZapOpts = append(opts.ZapOpts, zaplib.WrapCore(func(c zapcore.Core) zapcore.Core {
			return zapcore.NewTee(c, observability.NewZapCore(reporter))
		}))
	}
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

	restCfg := ctrl.GetConfigOrDie()

	// Design Q5: hard-fail at startup if the operator-global git credential is
	// configured but its source Secret is missing/incomplete — fail-closed rather
	// than silently degrade to anonymous git. This runs BEFORE the manager so the
	// manager cache isn't started yet; use a throwaway direct client.
	if cfg.GitAuth.Enabled() {
		// The operator's own namespace arrives via the downward API (POD_NAMESPACE);
		// the chart sets it. Without it we cannot locate the Secret.
		podNamespace := os.Getenv("POD_NAMESPACE")
		if podNamespace == "" {
			err := fmt.Errorf("gitAuth is enabled but POD_NAMESPACE is empty (set via the downward API)")
			setupLog.Error(err, "invalid operator environment")
			return err
		}
		directClient, cerr := client.New(restCfg, client.Options{Scheme: scheme})
		if cerr != nil {
			setupLog.Error(cerr, "unable to create startup client for gitAuth validation")
			return cerr
		}
		if verr := controller.ValidateGitAuthSecret(context.Background(), directClient, podNamespace, cfg.GitAuth); verr != nil {
			// verr names the Secret only, never its data.
			setupLog.Error(verr, "gitAuth secret validation failed")
			return verr
		}
	}

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: mgrOpts.MetricsBindAddress},
		HealthProbeBindAddress: mgrOpts.HealthProbeBindAddress,
		LeaderElection:         mgrOpts.LeaderElect,
		LeaderElectionID:       "app.baker.toggle-corp.com",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		return err
	}

	r := &controller.AppReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Config:           cfg,
		StorageClassName: mgrOpts.StorageClass,
		TraefikNamespace: mgrOpts.TraefikNamespace,
		Sentry:           reporter,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "App")
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
