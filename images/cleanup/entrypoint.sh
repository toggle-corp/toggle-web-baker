#!/usr/bin/env bash
# cleanup-entrypoint.sh -- prune the cache PVC when it is over threshold.
#
# Runs over the cache PVC mounted RW at /cache. Only cleans if the cache exceeds
# $CLEANUP_THRESHOLD_BYTES. Branches on $PACKAGE_MANAGER:
#   pnpm -> `pnpm store prune`  (content-addressed; reference-safe)
#   yarn -> trim the yarn cache (loose, regenerable)
#
# Reports the before/after byte counts to the termination message (<=4KB). No
# k8s API access (automountServiceAccountToken:false).
#
# Env in:
#   PACKAGE_MANAGER         (required) -- "pnpm" or "yarn".
#   CLEANUP_THRESHOLD_BYTES (required) -- only clean if du -sb /cache exceeds it.
set -euo pipefail

TERM_LOG="${TERMINATION_LOG:-/dev/termination-log}"
CACHE="${CACHE:-/cache}"

fail() {
	printf '%s\n' "cleanup: $1" | head -c 4000 >"$TERM_LOG" 2>/dev/null || true
	printf '%s\n' "cleanup: $1" >&2
	exit 1
}

[ -n "${PACKAGE_MANAGER:-}" ] || fail "PACKAGE_MANAGER is required"
[ -n "${CLEANUP_THRESHOLD_BYTES:-}" ] || fail "CLEANUP_THRESHOLD_BYTES is required"
case "$CLEANUP_THRESHOLD_BYTES" in
'' | *[!0-9]*) fail "CLEANUP_THRESHOLD_BYTES must be an integer" ;;
esac
[ -d "$CACHE" ] || fail "cache dir $CACHE not found"

du_bytes() { du -sb -- "$1" | awk '{print $1}'; }

before="$(du_bytes "$CACHE")"

if [ "$before" -le "$CLEANUP_THRESHOLD_BYTES" ]; then
	printf '{"action":"skip","reason":"under-threshold","before":%s,"threshold":%s}\n' \
		"$before" "$CLEANUP_THRESHOLD_BYTES" | head -c 4000 >"$TERM_LOG" 2>/dev/null || true
	printf 'cleanup: %s bytes <= threshold %s, nothing to do\n' \
		"$before" "$CLEANUP_THRESHOLD_BYTES" >&2
	exit 0
fi

action=""
case "$PACKAGE_MANAGER" in
pnpm)
	action="pnpm store prune"
	# pnpm's store lives under the cache PVC; point it there explicitly so the
	# prune operates on the mounted volume rather than a container-local store.
	export PNPM_HOME="$CACHE"
	pnpm config set store-dir "$CACHE/pnpm-store" >/dev/null 2>&1 || true
	# Reference-safe: removes only store entries no project references.
	pnpm store prune || fail "pnpm store prune failed"
	;;
yarn)
	action="yarn cache trim"
	# Yarn (classic) cache is loose and fully regenerable: clean it. Yarn Berry
	# uses a per-project cache; for a shared cache PVC we wipe the global cache
	# contents (NOT the dir itself, to keep the mount/ownership intact).
	if yarn cache clean >/dev/null 2>&1; then
		: # classic yarn cleaned its own cache dir
	fi
	# Belt-and-suspenders: trim the on-volume cache contents.
	if [ -d "$CACHE" ]; then
		find "$CACHE" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + ||
			fail "yarn cache trim failed"
	fi
	;;
*)
	fail "unsupported PACKAGE_MANAGER '$PACKAGE_MANAGER' (want pnpm or yarn)"
	;;
esac

after="$(du_bytes "$CACHE")"
reclaimed=$((before - after))

printf '{"action":"%s","before":%s,"after":%s,"reclaimed":%s,"threshold":%s}\n' \
	"$action" "$before" "$after" "$reclaimed" "$CLEANUP_THRESHOLD_BYTES" |
	head -c 4000 >"$TERM_LOG" 2>/dev/null || true
printf 'cleanup: %s reclaimed %s bytes (%s -> %s)\n' \
	"$action" "$reclaimed" "$before" "$after" >&2
