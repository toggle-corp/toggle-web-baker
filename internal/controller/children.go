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
)

// shortToken yields a filesystem/k8s-name-safe short hash of a rebuild token.
func shortToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:10]
}

// pvc builds a WaitForFirstConsumer PVC. local-path ignores requested capacity,
// but the field is required, so we request a nominal 1Gi.
func (r *FrontendAppReconciler) pvc(app *bakerv1alpha1.FrontendApp, name, storageClass string) *corev1.PersistentVolumeClaim {
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
// patch ONLY its own FrontendApp (the rebuild annotation). It does NOT create
// build Jobs.
func (r *FrontendAppReconciler) clockServiceAccount(app *bakerv1alpha1.FrontendApp) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: clockSAName(app), Namespace: app.Namespace, Labels: labelsFor(app)},
	}
}

func (r *FrontendAppReconciler) clockRole(app *bakerv1alpha1.FrontendApp) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: clockRoleName(app), Namespace: app.Namespace, Labels: labelsFor(app)},
		Rules: []rbacv1.PolicyRule{{
			APIGroups:     []string{bakerv1alpha1.GroupVersion.Group},
			Resources:     []string{"frontendapps"},
			Verbs:         []string{"get", "patch"},
			ResourceNames: []string{app.Name}, // scoped to THIS app only
		}},
	}
}

func (r *FrontendAppReconciler) clockRoleBinding(app *bakerv1alpha1.FrontendApp) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: clockBindingName(app), Namespace: app.Namespace, Labels: labelsFor(app)},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: clockSAName(app), Namespace: app.Namespace}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: clockRoleName(app)},
	}
}

// clockCronJob is the CLOCK: on each tick it patches the rebuild annotation with
// the current timestamp via kubectl. It never creates build Jobs.
func (r *FrontendAppReconciler) clockCronJob(app *bakerv1alpha1.FrontendApp) *batchv1.CronJob {
	patch := fmt.Sprintf(
		`kubectl annotate frontendapp %s %s="$(date +%%s)" --overwrite`,
		app.Name, bakerv1alpha1.RebuildAnnotation,
	)
	schedule := app.Spec.Schedule
	if schedule == "" {
		schedule = "0 */12 * * *"
	}
	podSpec := corev1.PodSpec{
		RestartPolicy:      corev1.RestartPolicyNever,
		ServiceAccountName: clockSAName(app),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   ptr.To(true),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{{
			Name:            "clock",
			Image:           r.Config.Images.Kubectl,
			Command:         []string{"sh", "-c", patch},
			SecurityContext: hardenedSecurityContext(),
		}},
	}
	if r.Config.ImagePullSecret != "" {
		podSpec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: r.Config.ImagePullSecret}}
	}
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: clockCronJobName(app), Namespace: app.Namespace, Labels: labelsFor(app)},
		Spec: batchv1.CronJobSpec{
			Schedule:                   schedule,
			ConcurrencyPolicy:          batchv1.ForbidConcurrent,
			SuccessfulJobsHistoryLimit: ptr.To(int32(1)),
			FailedJobsHistoryLimit:     ptr.To(int32(1)),
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					BackoffLimit: ptr.To(int32(2)),
					Template:     corev1.PodTemplateSpec{Spec: podSpec},
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

func (r *FrontendAppReconciler) nginxConfigMap(app *bakerv1alpha1.FrontendApp) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: nginxConfigName(app), Namespace: app.Namespace, Labels: labelsFor(app)},
		Data:       map[string]string{"default.conf": nginxConfTemplate},
	}
}

// nginxDeployment serves the bundle. Single replica, Recreate, mounts output PVC
// READ-ONLY. It co-locates by mounting the already-bound output PVC (RWO), so no
// explicit node affinity is needed.
func (r *FrontendAppReconciler) nginxDeployment(app *bakerv1alpha1.FrontendApp) *appsv1.Deployment {
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

func (r *FrontendAppReconciler) service(app *bakerv1alpha1.FrontendApp) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: serviceName(app), Namespace: app.Namespace, Labels: labelsFor(app)},
		Spec: corev1.ServiceSpec{
			Selector: nginxLabelsFor(app),
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(8080)}},
		},
	}
}

func (r *FrontendAppReconciler) ingress(app *bakerv1alpha1.FrontendApp) *networkingv1.Ingress {
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
	// Attach the basic-auth Middleware via the Traefik kubernetescrd annotation.
	if app.Spec.Auth != nil {
		ing.Annotations = map[string]string{
			"traefik.ingress.kubernetes.io/router.middlewares": fmt.Sprintf("%s-%s@kubernetescrd", app.Namespace, middlewareName(app)),
		}
	}
	return ing
}

// buildNetworkPolicy: build pod default-deny ingress; egress = DNS + 0.0.0.0/0
// EXCEPT the cluster pod/service CIDRs and the metadata IP.
func (r *FrontendAppReconciler) buildNetworkPolicy(app *bakerv1alpha1.FrontendApp) *networkingv1.NetworkPolicy {
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
func (r *FrontendAppReconciler) nginxNetworkPolicy(app *bakerv1alpha1.FrontendApp, traefikNamespace string) *networkingv1.NetworkPolicy {
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
