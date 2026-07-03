package view

import (
	"time"

	"github.com/robfig/cron/v3"
)

// nextScheduled derives the next fire time of the scheduled-builds trigger
// relative to now, formatted as RFC3339 in UTC. The operator's clock CronJob
// sets no spec.timeZone, so Kubernetes fires it in UTC — we compute in UTC to
// match. Disabled/absent yields "" (no scheduled builds); enabled with an EMPTY
// schedule also yields "" because the effective cron is the operator's config
// default, which the console cannot know (templates say "operator default"
// instead of guessing a time); an unparseable expression yields "" so the
// template renders an em-dash rather than a wrong time.
func nextScheduled(sb ScheduledBuilds) string {
	if !sb.Enabled || sb.Schedule == "" {
		return ""
	}
	sched, err := cron.ParseStandard(sb.Schedule)
	if err != nil {
		return ""
	}
	return sched.Next(Now()).UTC().Format(time.RFC3339)
}
