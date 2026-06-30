#!/usr/bin/env bash
# cleanup-entrypoint.sh -- reclaim space on a baker PVC. Branches on $MODE:
#
#   MODE=cache (default) -- prune the cache PVC mounted RW at /cache when it is
#     over $CLEANUP_THRESHOLD_BYTES. Branches on $PACKAGE_MANAGER:
#       pnpm -> `pnpm store prune`  (content-addressed; reference-safe)
#       yarn -> trim the yarn cache (loose, regenerable)
#   MODE=releases -- prune old release dirs under $RELEASES_DIR on the output
#     PVC, keeping the newest $KEEP_RELEASES plus any $PROTECTED_RELEASES.
#
# Reports a JSON status to the termination message (<=4KB). No k8s API access
# (automountServiceAccountToken:false).
#
# Env in (MODE=cache):
#   PACKAGE_MANAGER         (required) -- "pnpm" or "yarn".
#   CLEANUP_THRESHOLD_BYTES (required) -- only clean if du -sb /cache exceeds it.
#   CACHE                   (optional) -- cache mount (default /cache).
# Env in (MODE=releases):
#   RELEASES_DIR        (required) -- dir of release subdirs (copier layout).
#   KEEP_RELEASES       (required) -- integer count of newest releases to retain.
#   PROTECTED_RELEASES  (optional) -- comma-separated release dir names that are
#                                     NEVER deleted (e.g. current + previous).
set -euo pipefail

TERM_LOG="${TERMINATION_LOG:-/dev/termination-log}"
CACHE="${CACHE:-/cache}"
MODE="${MODE:-cache}"

fail() {
	printf '%s\n' "cleanup: $1" | head -c 4000 >"$TERM_LOG" 2>/dev/null || true
	printf '%s\n' "cleanup: $1" >&2
	exit 1
}

du_bytes() { du -sb -- "$1" | awk '{print $1}'; }

# ---- MODE=releases: prune old release dirs ----------------------------------
# Keep-set = (newest KEEP_RELEASES by dir name, which is a sortable timestamp in
# the copier's scheme) UNION PROTECTED_RELEASES. Delete everything else. Names
# are lexically==chronologically sortable, so `sort -r` is newest-first.
run_releases() {
	local rel_dir="${RELEASES_DIR:-}"
	[ -n "$rel_dir" ] || fail "RELEASES_DIR is required"
	[ -n "${KEEP_RELEASES:-}" ] || fail "KEEP_RELEASES is required"
	case "$KEEP_RELEASES" in
	'' | *[!0-9]*) fail "KEEP_RELEASES must be an integer" ;;
	esac
	local keep="$KEEP_RELEASES"

	# Defensive: missing dir -> nothing to do.
	if [ ! -d "$rel_dir" ]; then
		printf '{"action":"release-prune","kept":0,"deleted":0,"before":0,"after":0,"reclaimed":0}\n' |
			head -c 4000 >"$TERM_LOG" 2>/dev/null || true
		printf 'cleanup: releases dir %s not found, nothing to do\n' "$rel_dir" >&2
		exit 0
	fi

	local before
	before="$(du_bytes "$rel_dir")"

	# PROTECTED_RELEASES is a comma-separated set we never delete.
	local -A protected=()
	local IFS_OLD="$IFS" p
	IFS=','
	for p in ${PROTECTED_RELEASES:-}; do
		[ -n "$p" ] && protected["$p"]=1
	done
	IFS="$IFS_OLD"

	# Release dir basenames, newest first.
	local -a all=()
	local name
	while IFS= read -r name; do
		[ -n "$name" ] && all+=("$name")
	done < <(find "$rel_dir" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort -r)

	local kept=0 deleted=0
	for name in "${all[@]}"; do
		# Always keep protected releases (current + previous), even if old.
		if [ -n "${protected[$name]:-}" ]; then
			kept=$((kept + 1))
			continue
		fi
		# Keep the newest KEEP_RELEASES non-protected releases.
		if [ "$kept" -lt "$keep" ]; then
			kept=$((kept + 1))
			continue
		fi
		rm -rf -- "${rel_dir:?}/${name:?}" || fail "failed to remove release $name"
		deleted=$((deleted + 1))
		printf 'cleanup: pruned release %s\n' "$name" >&2
	done

	local after reclaimed
	after="$(du_bytes "$rel_dir")"
	reclaimed=$((before - after))

	printf '{"action":"release-prune","kept":%s,"deleted":%s,"before":%s,"after":%s,"reclaimed":%s}\n' \
		"$kept" "$deleted" "$before" "$after" "$reclaimed" |
		head -c 4000 >"$TERM_LOG" 2>/dev/null || true
	printf 'cleanup: release-prune kept %s deleted %s, reclaimed %s bytes (%s -> %s)\n' \
		"$kept" "$deleted" "$reclaimed" "$before" "$after" >&2
	exit 0
}

case "$MODE" in
cache) : ;; # fall through to the cache-prune logic below
releases) run_releases ;;
*) fail "unsupported MODE '$MODE' (want cache or releases)" ;;
esac

[ -n "${PACKAGE_MANAGER:-}" ] || fail "PACKAGE_MANAGER is required"
[ -n "${CLEANUP_THRESHOLD_BYTES:-}" ] || fail "CLEANUP_THRESHOLD_BYTES is required"
case "$CLEANUP_THRESHOLD_BYTES" in
'' | *[!0-9]*) fail "CLEANUP_THRESHOLD_BYTES must be an integer" ;;
esac
[ -d "$CACHE" ] || fail "cache dir $CACHE not found"

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
