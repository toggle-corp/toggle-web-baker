package controller

import (
	bakerv1alpha1 "github.com/toggle-corp/toggle-web-baker/api/v1alpha1"
)

// Child-object name suffixes, derived deterministically from the app name so
// every reconcile addresses the same objects.
func cacheePVCName(app *bakerv1alpha1.FrontendApp) string    { return app.Name + "-cache" }
func dataCachePVCName(app *bakerv1alpha1.FrontendApp) string { return app.Name + "-data-cache" }
func outputPVCName(app *bakerv1alpha1.FrontendApp) string    { return app.Name + "-output" }
func clockCronJobName(app *bakerv1alpha1.FrontendApp) string { return app.Name + "-clock" }
func watchCronJobName(app *bakerv1alpha1.FrontendApp) string { return app.Name + "-watch" }
func clockSAName(app *bakerv1alpha1.FrontendApp) string      { return app.Name + "-clock" }
func clockRoleName(app *bakerv1alpha1.FrontendApp) string    { return app.Name + "-clock" }
func clockBindingName(app *bakerv1alpha1.FrontendApp) string { return app.Name + "-clock" }
func nginxDeployName(app *bakerv1alpha1.FrontendApp) string  { return app.Name + "-nginx" }
func nginxConfigName(app *bakerv1alpha1.FrontendApp) string  { return app.Name + "-nginx-conf" }
func serviceName(app *bakerv1alpha1.FrontendApp) string      { return app.Name }
func ingressName(app *bakerv1alpha1.FrontendApp) string      { return app.Name }
func middlewareName(app *bakerv1alpha1.FrontendApp) string   { return app.Name + "-auth" }
func buildNetPolName(app *bakerv1alpha1.FrontendApp) string   { return app.Name + "-build" }
func nginxNetPolName(app *bakerv1alpha1.FrontendApp) string   { return app.Name + "-nginx" }
func triggerNetPolName(app *bakerv1alpha1.FrontendApp) string { return app.Name + "-trigger" }
// managedBy is the value of app.kubernetes.io/managed-by on every child.
const managedBy = "toggle-web-baker"

// labelsFor returns the standard selector/identity labels for app children.
func labelsFor(app *bakerv1alpha1.FrontendApp) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "frontendapp",
		"app.kubernetes.io/instance":   app.Name,
		"app.kubernetes.io/managed-by": managedBy,
	}
}

// buildLabelsFor adds the build-role label used to find active build pods/jobs.
func buildLabelsFor(app *bakerv1alpha1.FrontendApp) map[string]string {
	l := labelsFor(app)
	l["baker.toggle-corp.com/role"] = "build"
	return l
}

// nginxLabelsFor labels the serving Deployment + its pods.
func nginxLabelsFor(app *bakerv1alpha1.FrontendApp) map[string]string {
	l := labelsFor(app)
	l["baker.toggle-corp.com/role"] = "nginx"
	return l
}

// triggerLabelsFor labels the trigger (clock/watcher) CronJob pods so the
// trigger NetworkPolicy can select them.
func triggerLabelsFor(app *bakerv1alpha1.FrontendApp) map[string]string {
	l := labelsFor(app)
	l["baker.toggle-corp.com/role"] = "trigger"
	return l
}
