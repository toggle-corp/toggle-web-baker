// Command toggle-web-baker is the FrontendApp deploy-platform operator.
package main

import (
	"flag"
	"os"
	"strings"
	"time"

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

// stringSliceFlag collects repeatable / comma-separated string flags.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*s = append(*s, p)
		}
	}
	return nil
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
		storageClassName     string
		traefikNamespace     string

		registryAllowlist stringSliceFlag
		clusterCIDRs      stringSliceFlag
	)
	cfg := controller.OperatorConfig{}

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "metrics endpoint bind address")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "health probe bind address")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true, "enable leader election for HA")
	flag.StringVar(&storageClassName, "storage-class", "", "WaitForFirstConsumer StorageClass backing all PVCs")
	flag.StringVar(&traefikNamespace, "traefik-namespace", "traefik", "namespace of the Traefik controller (nginx ingress NetworkPolicy)")

	flag.Var(&registryAllowlist, "registry-allowlist", "allowed image prefixes (repeatable / comma-separated)")
	flag.Var(&clusterCIDRs, "cluster-cidrs", "MANDATORY pod+service CIDRs excluded from build-pod egress")
	flag.StringVar(&cfg.TraefikGroup, "traefik-group", "traefik.io", "API group of the Traefik Middleware CRD")
	flag.StringVar(&cfg.ImagePullSecret, "image-pull-secret", "", "imagePullSecret stamped onto all platform pods")
	flag.DurationVar(&cfg.MeasureInterval, "storage-measure-interval", time.Hour, "debounce floor between post-build du storage measurements")

	flag.StringVar(&cfg.Images.Clone, "image-clone", "", "digest-pinned clone image")
	flag.StringVar(&cfg.Images.Copier, "image-copier", "", "digest-pinned copier image")
	flag.StringVar(&cfg.Images.Du, "image-du", "", "digest-pinned du-measurement image")
	flag.StringVar(&cfg.Images.Cleanup, "image-cleanup", "", "digest-pinned cleanup image")
	flag.StringVar(&cfg.Images.Clock, "image-clock", "", "digest-pinned clock image for the CronJob tick")
	flag.StringVar(&cfg.Images.Nginx, "image-nginx", "", "nginx serving image")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	cfg.RegistryAllowlist = registryAllowlist
	cfg.ClusterCIDRs = clusterCIDRs
	cfg.Defaults()

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
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
		StorageClassName: storageClassName,
		TraefikNamespace: traefikNamespace,
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
