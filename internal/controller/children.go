package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
	"github.com/toggle-corp/toggle-web-baker/internal/domain"
)

// shortToken yields a filesystem/k8s-name-safe short hash of a rebuild token.
func shortToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:10]
}

// pvc builds a WaitForFirstConsumer PVC. local-path ignores requested capacity,
// but the field is required, so we request a nominal 1Gi.
func (r *AppReconciler) pvc(app *bakerv1alpha1.App, name, storageClass string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: app.Namespace, Labels: labelsFor(app)},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: ptr.To(storageClass),
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: mustQuantity("1Gi")},
			},
		},
	}
}

// clockServiceAccount + Role + RoleBinding give the CronJob clock permission to
// patch ONLY its own App (the rebuild annotation). It does NOT create
// build Jobs.
func (r *AppReconciler) clockServiceAccount(app *bakerv1alpha1.App) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: clockSAName(app), Namespace: app.Namespace, Labels: labelsFor(app)},
	}
}

func (r *AppReconciler) clockRole(app *bakerv1alpha1.App) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: clockRoleName(app), Namespace: app.Namespace, Labels: labelsFor(app)},
		Rules: []rbacv1.PolicyRule{{
			APIGroups:     []string{bakerv1alpha1.GroupVersion.Group},
			Resources:     []string{bakerv1alpha1.AppResource},
			Verbs:         []string{"get", "patch"},
			ResourceNames: []string{app.Name}, // scoped to THIS app only
		}},
	}
}

func (r *AppReconciler) clockRoleBinding(app *bakerv1alpha1.App) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: clockBindingName(app), Namespace: app.Namespace, Labels: labelsFor(app)},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: clockSAName(app), Namespace: app.Namespace}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: clockRoleName(app)},
	}
}

// clockCronJob is the CLOCK: on each tick it patches the rebuild annotation with
// the current timestamp via kubectl. It never creates build Jobs. Rendered only
// when spec.scheduledBuilds is enabled.
func (r *AppReconciler) clockCronJob(app *bakerv1alpha1.App) *batchv1.CronJob {
	schedule := app.Spec.ScheduledBuilds.Schedule
	if schedule == "" {
		schedule = r.Config.DefaultSchedule
	}
	// The tick logic lives in the platform-owned clock image's entrypoint: it
	// sets requested-at AND clears any stale "by"/"commit" in the SAME annotate
	// call, so a scheduled tick can't be mislabeled Manual or Commit by leftovers
	// (the operator classifies trigger by which key is present). The operator
	// passes the app name and annotation keys via env so api/v1alpha1 stays the
	// single source of truth for the keys.
	return r.triggerCronJob(app, clockCronJobName(app), schedule, nil)
}

// watchCronJob is the commit WATCHER: on each tick it polls `git ls-remote` on
// the app's repo/ref (clock image, MODE=watch) and requests a rebuild — stamping
// the commit annotation — only when the SHA changed since the last poll. It
// shares the clock's image, ServiceAccount, and scoped RBAC. Rendered only when
// spec.watchCommits is enabled.
func (r *AppReconciler) watchCronJob(app *bakerv1alpha1.App, gitCred gitCredentialDecision) (*batchv1.CronJob, error) {
	interval := app.Spec.WatchCommits.Interval
	if interval == "" {
		interval = r.Config.DefaultWatchInterval
	}
	schedule, err := domain.WatchCron(interval)
	if err != nil {
		return nil, err
	}
	ref := app.Spec.Ref
	if ref == "" {
		ref = "HEAD"
	}
	watchEnv := []corev1.EnvVar{
		{Name: "MODE", Value: "watch"},
		{Name: "REPO", Value: app.Spec.Repo},
		{Name: "REF", Value: ref},
		{Name: "LAST_SEEN_ANNOTATION", Value: bakerv1alpha1.WatchLastSeenAnnotation},
	}
	cj := r.triggerCronJob(app, watchCronJobName(app), schedule, watchEnv)
	// Git credential (design Q3/Q4/Q6/Q7): the watcher runs `git ls-remote`
	// against the user-controlled spec.repo, so it authenticates with the SAME
	// threaded decision as the clone phase (F1 — never re-derived). Only the WATCH
	// CronJob mounts it — the clock (scheduled-builds) CronJob never touches the
	// repo, so it stays credential-free. Wired via the shared addGitCredential
	// helper, host-scoped (GIT_CREDENTIAL_HOST); host="" fails closed (anonymous).
	if gitCred.mounts() {
		host, _ := domain.RepoHost(app.Spec.Repo)
		addGitCredential(&cj.Spec.JobTemplate.Spec.Template.Spec, "clock", gitCred.secretName, host)
	}
	return cj, nil
}

// triggerCronJob renders one trigger CronJob (clock tick or commit watcher):
// same image, shared clock ServiceAccount, same hardening; only name, schedule
// and extra env differ.
func (r *AppReconciler) triggerCronJob(app *bakerv1alpha1.App, name, schedule string, extraEnv []corev1.EnvVar) *batchv1.CronJob {
	env := []corev1.EnvVar{
		{Name: "APP", Value: app.Name},
		// The group-qualified kubectl target — env-passed like the annotation
		// keys so the clock image never hardcodes the CRD's name.
		{Name: "RESOURCE", Value: bakerv1alpha1.AppResource + "." + bakerv1alpha1.GroupVersion.Group},
		{Name: "REQUESTED_AT_ANNOTATION", Value: bakerv1alpha1.RebuildAnnotation},
		{Name: "BY_ANNOTATION", Value: bakerv1alpha1.RebuildByAnnotation},
		{Name: "COMMIT_ANNOTATION", Value: bakerv1alpha1.RebuildCommitAnnotation},
		// kubectl's discovery cache needs a writable HOME under the pod's
		// readOnlyRootFilesystem; point it at the tmp emptyDir below.
		{Name: "HOME", Value: "/tmp"},
	}
	env = append(env, extraEnv...)
	podSpec := corev1.PodSpec{
		RestartPolicy:      corev1.RestartPolicyNever,
		ServiceAccountName: clockSAName(app),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   ptr.To(true),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{{
			Name:            "clock",
			Image:           r.Config.Images.Clock,
			Env:             env,
			VolumeMounts:    []corev1.VolumeMount{{Name: volTmp, MountPath: "/tmp"}},
			SecurityContext: clockSecurityContext(),
		}},
		Volumes: []corev1.Volume{
			{Name: volTmp, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
	}
	if r.Config.ImagePullSecret != "" {
		podSpec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: r.Config.ImagePullSecret}}
	}
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: app.Namespace, Labels: labelsFor(app)},
		Spec: batchv1.CronJobSpec{
			Schedule:                   schedule,
			ConcurrencyPolicy:          batchv1.ForbidConcurrent,
			SuccessfulJobsHistoryLimit: ptr.To(int32(1)),
			FailedJobsHistoryLimit:     ptr.To(int32(1)),
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					BackoffLimit: ptr.To(int32(2)),
					Template: corev1.PodTemplateSpec{
						// The role=trigger label is what triggerNetworkPolicy selects.
						ObjectMeta: metav1.ObjectMeta{Labels: triggerLabelsFor(app)},
						Spec:       podSpec,
					},
				},
			},
		},
	}
}

// triggerNetworkPolicy fences the trigger (clock/watcher) pods the same way
// buildNetworkPolicy fences build pods: default-deny ingress; egress = DNS +
// 0.0.0.0/0 EXCEPT the cluster pod/service CIDRs and the metadata IP. The
// watcher runs `git ls-remote` against the USER-controlled spec.repo URL, so it
// needs the same SSRF fencing as the clone container that fetches the same URL.
// kubectl still reaches the apiserver: the service VIP is resolved to the
// control-plane endpoint (a node IP outside the excluded pod/service CIDRs)
// before policy evaluation.
func (r *AppReconciler) triggerNetworkPolicy(app *bakerv1alpha1.App) *networkingv1.NetworkPolicy {
	except := append([]string{}, r.Config.ClusterCIDRs...)
	except = append(except, MetadataIP)
	dnsPort := intstr.FromInt32(53)
	udp := corev1.ProtocolUDP
	tcp := corev1.ProtocolTCP
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: triggerNetPolName(app), Namespace: app.Namespace, Labels: labelsFor(app)},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"baker.toggle-corp.com/role": "trigger", "app.kubernetes.io/instance": app.Name}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			// No Ingress rules => default-deny ingress.
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{ // DNS to the cluster resolver ONLY (kube-system / k8s-app=kube-dns).
					To: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"},
						},
						PodSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"k8s-app": "kube-dns"},
						},
					}},
					Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &dnsPort}, {Protocol: &tcp, Port: &dnsPort}},
				},
				{ // public internet + apiserver endpoint, minus cluster CIDRs + metadata
					To: []networkingv1.NetworkPolicyPeer{{
						IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: except},
					}},
				},
			},
		},
	}
}

const nginxConfTemplate = `server {
    listen 8080;
    root /output/current;
    disable_symlinks if_not_owner;
    location / {
        try_files $uri $uri/ /index.html;
    }
}
`

func (r *AppReconciler) nginxConfigMap(app *bakerv1alpha1.App) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: nginxConfigName(app), Namespace: app.Namespace, Labels: labelsFor(app)},
		Data:       map[string]string{"default.conf": nginxConfTemplate},
	}
}

// nginxDeployment serves the bundle. Single replica, Recreate, mounts output PVC
// READ-ONLY. It co-locates by mounting the already-bound output PVC (RWO), so no
// explicit node affinity is needed.
func (r *AppReconciler) nginxDeployment(app *bakerv1alpha1.App) *appsv1.Deployment {
	labels := nginxLabelsFor(app)
	podSpec := corev1.PodSpec{
		AutomountServiceAccountToken: ptr.To(false),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   ptr.To(true),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{{
			Name:  "nginx",
			Image: r.Config.Images.Nginx,
			Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
			VolumeMounts: []corev1.VolumeMount{
				{Name: volOutput, MountPath: outputMountPath, ReadOnly: true},
				{Name: "nginx-conf", MountPath: "/etc/nginx/conf.d"},
				{Name: volTmp, MountPath: "/tmp"},
				{Name: "nginx-cache", MountPath: "/var/cache/nginx"},
				{Name: "nginx-run", MountPath: "/var/run"},
			},
			SecurityContext: nginxSecurityContext(),
		}},
		Volumes: []corev1.Volume{
			{Name: volOutput, VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: outputPVCName(app), ReadOnly: true}}},
			{Name: "nginx-conf", VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: nginxConfigName(app)}}}},
			{Name: volTmp, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			{Name: "nginx-cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			{Name: "nginx-run", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
	}
	if r.Config.ImagePullSecret != "" {
		podSpec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: r.Config.ImagePullSecret}}
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: nginxDeployName(app), Namespace: app.Namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
}

func (r *AppReconciler) service(app *bakerv1alpha1.App) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: serviceName(app), Namespace: app.Namespace, Labels: labelsFor(app)},
		Spec: corev1.ServiceSpec{
			Selector: nginxLabelsFor(app),
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(8080)}},
		},
	}
}

func (r *AppReconciler) ingress(app *bakerv1alpha1.App) *networkingv1.Ingress {
	pathType := networkingv1.PathTypePrefix
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: ingressName(app), Namespace: app.Namespace, Labels: labelsFor(app)},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{
				Host: app.Spec.Ingress.Host,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     "/",
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: serviceName(app),
									Port: networkingv1.ServiceBackendPort{Number: 80},
								},
							},
						}},
					},
				},
			}},
		},
	}
	if app.Spec.Ingress.ClassName != nil {
		ing.Spec.IngressClassName = app.Spec.Ingress.ClassName
	}
	if app.Spec.Ingress.TLS != nil {
		ing.Spec.TLS = []networkingv1.IngressTLS{{
			Hosts:      []string{app.Spec.Ingress.Host},
			SecretName: app.Spec.Ingress.TLS.SecretName,
		}}
	}
	// Merge user-supplied annotations FIRST, then overlay operator-managed keys
	// LAST so the operator always wins on a conflict. The router.middlewares key
	// is CEL-rejected in spec.ingress.annotations, but overlaying it last is
	// defense in depth (a user value can never strip/redirect basic-auth).
	if len(app.Spec.Ingress.Annotations) > 0 {
		ing.Annotations = make(map[string]string, len(app.Spec.Ingress.Annotations)+1)
		for k, v := range app.Spec.Ingress.Annotations {
			ing.Annotations[k] = v
		}
	}
	// Attach the basic-auth Middleware via the Traefik kubernetescrd annotation.
	if app.Spec.Auth != nil {
		if ing.Annotations == nil {
			ing.Annotations = map[string]string{}
		}
		ing.Annotations["traefik.ingress.kubernetes.io/router.middlewares"] = fmt.Sprintf("%s-%s@kubernetescrd", app.Namespace, middlewareName(app))
	}
	return ing
}

// buildNetworkPolicy: build pod default-deny ingress; egress = DNS + 0.0.0.0/0
// EXCEPT the cluster pod/service CIDRs and the metadata IP.
func (r *AppReconciler) buildNetworkPolicy(app *bakerv1alpha1.App) *networkingv1.NetworkPolicy {
	except := append([]string{}, r.Config.ClusterCIDRs...)
	except = append(except, MetadataIP)
	dnsPort := intstr.FromInt32(53)
	udp := corev1.ProtocolUDP
	tcp := corev1.ProtocolTCP
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: buildNetPolName(app), Namespace: app.Namespace, Labels: labelsFor(app)},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"baker.toggle-corp.com/role": "build", "app.kubernetes.io/instance": app.Name}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			// No Ingress rules => default-deny ingress.
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{ // DNS to the cluster resolver ONLY (kube-system / k8s-app=kube-dns).
					// Without a To peer, :53 would be open to EVERYTHING — including
					// the excepted cluster CIDRs and the metadata IP — defeating the
					// second rule's exclusions.
					To: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"},
						},
						PodSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"k8s-app": "kube-dns"},
						},
					}},
					Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &dnsPort}, {Protocol: &tcp, Port: &dnsPort}},
				},
				{ // public internet, minus cluster CIDRs + metadata
					To: []networkingv1.NetworkPolicyPeer{{
						IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: except},
					}},
				},
			},
		},
	}
}

// nginxNetworkPolicy: ingress only from the Traefik controller namespace/pods,
// egress DNS only.
func (r *AppReconciler) nginxNetworkPolicy(app *bakerv1alpha1.App, traefikNamespace string) *networkingv1.NetworkPolicy {
	dnsPort := intstr.FromInt32(53)
	httpPort := intstr.FromInt32(8080)
	udp := corev1.ProtocolUDP
	tcp := corev1.ProtocolTCP
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: nginxNetPolName(app), Namespace: app.Namespace, Labels: labelsFor(app)},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"baker.toggle-corp.com/role": "nginx", "app.kubernetes.io/instance": app.Name}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"kubernetes.io/metadata.name": traefikNamespace},
					},
				}},
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &httpPort}},
			}},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &dnsPort}, {Protocol: &tcp, Port: &dnsPort}},
			}},
		},
	}
}
