package domain

import (
	"fmt"
	"time"
)

// WatchCron converts a commit-watch poll interval (a Go duration string from
// spec.watchCommits.interval or the operator default) into the cron schedule
// for the watcher CronJob. CronJob schedules can't express arbitrary periods,
// so the interval must land on a cron-expressible grid: whole minutes in
// [1m, 59m] ("*/M * * * *", uneven divisors of 60 give a slightly lumpy but
// harmless poll rhythm) or whole hours ("0 */H * * *"). Anything else —
// sub-minute, non-whole-minute, or >59m that isn't whole hours — is rejected
// so a bad value fails loudly instead of polling at a surprising rate.
func WatchCron(interval string) (string, error) {
	d, err := time.ParseDuration(interval)
	if err != nil {
		return "", fmt.Errorf("watch interval %q is not a valid duration: %w", interval, err)
	}
	if d < time.Minute {
		return "", fmt.Errorf("watch interval %q must be at least 1m", interval)
	}
	if d%time.Minute != 0 {
		return "", fmt.Errorf("watch interval %q must be whole minutes", interval)
	}
	minutes := int(d / time.Minute)
	if minutes <= 59 {
		return fmt.Sprintf("*/%d * * * *", minutes), nil
	}
	if d%time.Hour != 0 {
		return "", fmt.Errorf("watch interval %q above 59m must be whole hours", interval)
	}
	return fmt.Sprintf("0 */%d * * *", int(d/time.Hour)), nil
}
