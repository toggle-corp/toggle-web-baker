package view

import "math"

// StorageBarNoBar is the sentinel pct returned when no capacity is known, so
// the template can choose to render no bar (rather than a misleading 0%/100%).
const StorageBarNoBar = -1

// StorageBar computes a fill percentage for a used/capacity pair. pct is
// round(used/cap*100); over reports used>cap (so the template can flag a
// breach even though pct may exceed 100). A non-positive capBytes returns the
// StorageBarNoBar sentinel — there is nothing to draw against.
func StorageBar(usedBytes, capBytes int64) (pct int, over bool) {
	if capBytes <= 0 {
		return StorageBarNoBar, false
	}
	pct = int(math.Round(float64(usedBytes) / float64(capBytes) * 100))
	return pct, usedBytes > capBytes
}
