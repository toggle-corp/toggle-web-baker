package controller

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// Trigger children (clock + watcher CronJobs and their shared RBAC) are opt-in:
// an app with neither scheduledBuilds nor watchCommits gets none of them, and
// builds happen only via bootstrap/manual.
func TestTriggers_BothDisabled_NoCronJobsNoRBAC(t *testing.T) {
	app := baseApp()
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app)

	for name, obj := range map[string]client.Object{
		clockCronJobName(app): &batchv1.CronJob{},
		watchCronJobName(app): &batchv1.CronJob{},
		clockSAName(app):      &corev1.ServiceAccount{},
		clockRoleName(app):    &rbacv1.Role{},
		clockBindingName(app): &rbacv1.RoleBinding{},
	} {
		err := cl.Get(context.Background(), types.NamespacedName{Name: name, Namespace: app.Namespace}, obj)
		if err == nil {
			t.Errorf("child %s exists; want absent when both triggers are disabled", name)
		}
	}
}

func getCronJob(t *testing.T, cl client.Client, name, ns string) *batchv1.CronJob {
	t.Helper()
	cj := &batchv1.CronJob{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: name, Namespace: ns}, cj); err != nil {
		t.Fatalf("get CronJob %s: %v", name, err)
	}
	return cj
}

func cronEnvMap(t *testing.T, cj *batchv1.CronJob) map[string]string {
	t.Helper()
	containers := cj.Spec.JobTemplate.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("CronJob %s: want 1 container, got %d", cj.Name, len(containers))
	}
	m := map[string]string{}
	for _, e := range containers[0].Env {
		m[e.Name] = e.Value
	}
	return m
}

func TestTriggers_ScheduledEnabled_ClockUsesOperatorDefault(t *testing.T) {
	app := baseApp()
	app.Spec.ScheduledBuilds = &bakerv1alpha1.ScheduledBuildsSpec{Enabled: true}
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app)

	cj := getCronJob(t, cl, clockCronJobName(app), app.Namespace)
	if cj.Spec.Schedule != "0 */12 * * *" {
		t.Errorf("clock schedule = %q, want operator default 0 */12 * * *", cj.Spec.Schedule)
	}
	env := cronEnvMap(t, cj)
	if env["COMMIT_ANNOTATION"] != bakerv1alpha1.RebuildCommitAnnotation {
		t.Errorf("clock COMMIT_ANNOTATION = %q, want %q", env["COMMIT_ANNOTATION"], bakerv1alpha1.RebuildCommitAnnotation)
	}
	// Watcher stays absent.
	if err := cl.Get(context.Background(), types.NamespacedName{Name: watchCronJobName(app), Namespace: app.Namespace}, &batchv1.CronJob{}); err == nil {
		t.Error("watcher CronJob exists; want absent when watchCommits is disabled")
	}
}

func TestTriggers_ScheduledEnabled_CustomSchedule(t *testing.T) {
	app := baseApp()
	app.Spec.ScheduledBuilds = &bakerv1alpha1.ScheduledBuildsSpec{Enabled: true, Schedule: "15 3 * * *"}
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app)

	if got := getCronJob(t, cl, clockCronJobName(app), app.Namespace).Spec.Schedule; got != "15 3 * * *" {
		t.Errorf("clock schedule = %q, want 15 3 * * *", got)
	}
}

func TestTriggers_WatchEnabled_WatcherRenderedWithContractEnv(t *testing.T) {
	app := baseApp()
	app.Spec.Ref = "main"
	app.Spec.WatchCommits = &bakerv1alpha1.WatchCommitsSpec{Enabled: true, Interval: "5m"}
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app)

	cj := getCronJob(t, cl, watchCronJobName(app), app.Namespace)
	if cj.Spec.Schedule != "*/5 * * * *" {
		t.Errorf("watcher schedule = %q, want */5 * * * *", cj.Spec.Schedule)
	}
	env := cronEnvMap(t, cj)
	want := map[string]string{
		"MODE":                    "watch",
		"APP":                     app.Name,
		"REPO":                    app.Spec.Repo,
		"REF":                     "main",
		"REQUESTED_AT_ANNOTATION": bakerv1alpha1.RebuildAnnotation,
		"BY_ANNOTATION":           bakerv1alpha1.RebuildByAnnotation,
		"COMMIT_ANNOTATION":       bakerv1alpha1.RebuildCommitAnnotation,
		"LAST_SEEN_ANNOTATION":    bakerv1alpha1.WatchLastSeenAnnotation,
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("watcher env %s = %q, want %q", k, env[k], v)
		}
	}
	if cj.Spec.JobTemplate.Spec.Template.Spec.ServiceAccountName != clockSAName(app) {
		t.Errorf("watcher SA = %q, want shared clock SA %q", cj.Spec.JobTemplate.Spec.Template.Spec.ServiceAccountName, clockSAName(app))
	}
	// Shared RBAC exists even though scheduledBuilds is disabled.
	if err := cl.Get(context.Background(), types.NamespacedName{Name: clockSAName(app), Namespace: app.Namespace}, &corev1.ServiceAccount{}); err != nil {
		t.Errorf("clock SA absent, want present for watcher: %v", err)
	}
	// Clock stays absent.
	if err := cl.Get(context.Background(), types.NamespacedName{Name: clockCronJobName(app), Namespace: app.Namespace}, &batchv1.CronJob{}); err == nil {
		t.Error("clock CronJob exists; want absent when scheduledBuilds is disabled")
	}
}

func TestTriggers_WatchEnabled_DefaultIntervalAndRef(t *testing.T) {
	app := baseApp() // Ref empty (fake client applies no CRD defaults)
	app.Spec.WatchCommits = &bakerv1alpha1.WatchCommitsSpec{Enabled: true}
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app)

	cj := getCronJob(t, cl, watchCronJobName(app), app.Namespace)
	if cj.Spec.Schedule != "*/10 * * * *" {
		t.Errorf("watcher schedule = %q, want operator default */10 * * * *", cj.Spec.Schedule)
	}
	if env := cronEnvMap(t, cj); env["REF"] != "HEAD" {
		t.Errorf("watcher REF = %q, want HEAD fallback", env["REF"])
	}
}

// Disabling a trigger after it ran must garbage-collect its CronJob; disabling
// both also reclaims the shared RBAC.
func TestTriggers_DisableAfterEnable_ChildrenDeleted(t *testing.T) {
	app := baseApp()
	app.Spec.ScheduledBuilds = &bakerv1alpha1.ScheduledBuildsSpec{Enabled: true}
	app.Spec.WatchCommits = &bakerv1alpha1.WatchCommitsSpec{Enabled: true}
	r, cl := newReconciler(t, app, wffc())
	reconcile(t, r, app)
	getCronJob(t, cl, clockCronJobName(app), app.Namespace)
	getCronJob(t, cl, watchCronJobName(app), app.Namespace)

	// Flip watchCommits off.
	live := getApp(t, cl, app.Name, app.Namespace)
	live.Spec.WatchCommits.Enabled = false
	if err := cl.Update(context.Background(), live); err != nil {
		t.Fatalf("update app: %v", err)
	}
	reconcile(t, r, app)
	if err := cl.Get(context.Background(), types.NamespacedName{Name: watchCronJobName(app), Namespace: app.Namespace}, &batchv1.CronJob{}); err == nil {
		t.Error("watcher CronJob survived watchCommits.enabled=false")
	}
	getCronJob(t, cl, clockCronJobName(app), app.Namespace) // clock unaffected

	// Flip scheduledBuilds off too: clock AND shared RBAC go.
	live = getApp(t, cl, app.Name, app.Namespace)
	live.Spec.ScheduledBuilds.Enabled = false
	if err := cl.Update(context.Background(), live); err != nil {
		t.Fatalf("update app: %v", err)
	}
	reconcile(t, r, app)
	for name, obj := range map[string]client.Object{
		clockCronJobName(app): &batchv1.CronJob{},
		clockSAName(app):      &corev1.ServiceAccount{},
		clockRoleName(app):    &rbacv1.Role{},
		clockBindingName(app): &rbacv1.RoleBinding{},
	} {
		if err := cl.Get(context.Background(), types.NamespacedName{Name: name, Namespace: app.Namespace}, obj); err == nil {
			t.Errorf("child %s survived disabling both triggers", name)
		}
	}
}

// An interval the CRD pattern admits but a CronJob cannot express (e.g. 90m)
// must fail the reconcile loudly, not render a surprising schedule.
func TestTriggers_UnexpressibleInterval_ReconcileErrors(t *testing.T) {
	app := baseApp()
	app.Spec.WatchCommits = &bakerv1alpha1.WatchCommitsSpec{Enabled: true, Interval: "90m"}
	r, _ := newReconciler(t, app, wffc())
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: app.Name, Namespace: app.Namespace}})
	if err == nil {
		t.Fatal("reconcile with 90m interval succeeded; want error")
	}
}
