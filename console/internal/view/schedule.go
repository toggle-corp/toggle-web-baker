package view

import (
	"time"

	"github.com/robfig/cron/v3"
)

// DefaultSchedule mirrors the operator's clock CronJob default in
// internal/controller/children.go (and the kubebuilder default on
// FrontendApp.Spec.Schedule). The console is a separate Go module and does not
// import the operator's types, so this constant is duplicated deliberately;
// keep it in sync. A drift only mis-renders the "Next scheduled" countdown, it
// never affects real scheduling (the CronJob owns that).
const DefaultSchedule = "0 */12 * * *"

// nextScheduled derives the next fire time of a cron expression relative to
// now, formatted as RFC3339 in UTC. The operator's clock CronJob sets no
// spec.timeZone, so Kubernetes fires it in UTC — we compute in UTC to match.
// An empty schedule falls back to DefaultSchedule; an unparseable one yields ""
// so the template renders an em-dash rather than a wrong time.
func nextScheduled(schedule string) string {
	if schedule == "" {
		schedule = DefaultSchedule
	}
	sched, err := cron.ParseStandard(schedule)
	if err != nil {
		return ""
	}
	return sched.Next(Now()).UTC().Format(time.RFC3339)
}
