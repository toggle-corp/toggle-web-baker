package view

import (
	"fmt"
)

// Humanizers are pure (modulo the overridable Now var) so the templates can
// stay logic-free. All parsing is defensive: bad input yields a dash, not a panic.

// HumanizeBytes renders n in IEC units (B, KiB, MiB, GiB, TiB). Values below
// 1 KiB are shown as whole bytes; larger values carry one decimal. The sign is
// preserved for negative inputs (used by delta rendering).
func HumanizeBytes(n int64) string {
	if n == 0 {
		return "0 B"
	}
	neg := n < 0
	abs := n
	if neg {
		abs = -n
	}
	const unit = 1024
	if abs < unit {
		if neg {
			return fmt.Sprintf("-%d B", abs)
		}
		return fmt.Sprintf("%d B", abs)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(abs)
	idx := -1
	for value >= unit && idx < len(units)-1 {
		value /= unit
		idx++
	}
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%.1f %s", sign, value, units[idx])
}

// HumanizeDelta renders a signed byte delta with a direction marker:
// "▲ +120.0 MiB", "▼ -1.2 GiB", or "▬ no change" for zero.
func HumanizeDelta(n int64) string {
	switch {
	case n == 0:
		return "▬ no change"
	case n > 0:
		return "▲ +" + HumanizeBytes(n)
	default:
		// HumanizeBytes already prefixes the minus sign.
		return "▼ " + HumanizeBytes(n)
	}
}
