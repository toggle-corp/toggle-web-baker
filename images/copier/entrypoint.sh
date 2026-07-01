#!/usr/bin/env bash
# copier-entrypoint.sh -- THE critical, output-mutating phase.
#
# Runs as the MAIN container, AFTER all initContainers (clone -> setup -> fetch
# -> build) have succeeded. It is the ONLY writer to the output PVC at /output.
#
# Order of operations is load-bearing (see README "gate ordering"):
#   a. Retention sweep   -- reclaim space BEFORE measuring (race-free).
#   b. Size gate         -- du -sb the SOURCE on the work volume; reject > cap.
#   c. Free-space gate   -- df /output; require source+headroom <= free.
#   d. Assemble          -- rsync -a --safe-links into a new release dir; chown.
#   e. Flip gate         -- re-check assembled size <= cap (defense in depth).
#   f. Atomic flip        -- ln -sfn current.tmp && mv -T current.tmp current.
#   g. Termination JSON   -- status blob to /dev/termination-log (<4KB).
#
# Pods have automountServiceAccountToken:false: ALL results go to the operator
# via the termination-message file, never the k8s API.
#
# Env in:
#   OUTPUT_DIR          -- subdir of /workspace holding the build output (source).
#   RELEASE_SIZE_CAP    -- hard byte cap on a release (0 = unset).
#   FREE_HEADROOM_BYTES -- bytes that must remain free after the copy (default 0).
#   KEEP_RELEASES       -- non-current releases to retain (default 0).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=copier/lib.sh
. "$SCRIPT_DIR/lib.sh"

# ---- config / defaults ------------------------------------------------------
WORKSPACE="${WORKSPACE:-/workspace}"
OUTPUT_ROOT="${OUTPUT_ROOT:-/output}"
RELEASES_DIR="$OUTPUT_ROOT/releases"
PHASE_ENV="${PHASE_ENV:-$WORKSPACE/phase-env}"
TERM_LOG="${TERMINATION_LOG:-/dev/termination-log}"

OUTPUT_DIR="${OUTPUT_DIR:?OUTPUT_DIR is required}"
RELEASE_SIZE_CAP="${RELEASE_SIZE_CAP:-0}"
FREE_HEADROOM_BYTES="${FREE_HEADROOM_BYTES:-0}"
KEEP_RELEASES="${KEEP_RELEASES:-0}"
# Platform user that owns assembled files (nginx disable_symlinks if_not_owner).
PLATFORM_OWNER="${PLATFORM_OWNER:-65532:65532}"

SOURCE_DIR="$WORKSPACE/$OUTPUT_DIR"
# Sortable, lexical==chronological timestamp; UTC; collision-safe via PID.
TS_FORMAT='%Y%m%dT%H%M%SZ'
RELEASE_TS="$(date -u +"$TS_FORMAT")-$$"
RELEASE_REL="releases/$RELEASE_TS"
RELEASE_ABS="$OUTPUT_ROOT/$RELEASE_REL"

# die "<reason>" -- emit a JSON error blob to the termination log and exit 1.
die() {
	local reason
	reason="$(json_escape "$1")"
	printf '{"error":"%s","releaseTs":"%s"}\n' "$reason" "$RELEASE_TS" |
		head -c 4000 >"$TERM_LOG" 2>/dev/null || true
	printf 'copier: %s\n' "$1" >&2
	exit 1
}

# current_target_basename -- basename of the release `current` points at, or "".
current_target_basename() {
	local link="$OUTPUT_ROOT/current" tgt
	[ -L "$link" ] || return 0
	tgt="$(readlink "$link" 2>/dev/null || true)"
	[ -n "$tgt" ] && printf '%s' "${tgt##*/}"
}

main() {
	[ -d "$SOURCE_DIR" ] || die "source $SOURCE_DIR does not exist"
	mkdir -p -- "$RELEASES_DIR" || die "cannot create $RELEASES_DIR"

	local keep_current
	keep_current="$(current_target_basename)"

	# (a) Retention sweep BEFORE measuring -- reclaim space race-free.
	retention_sweep "$RELEASES_DIR" "$KEEP_RELEASES" "$keep_current" ||
		die "retention sweep failed"

	# (b) Pre-copy size gate on the SOURCE (work volume), before any write to /output.
	local src_bytes
	src_bytes="$(du_bytes "$SOURCE_DIR")" || die "du of source failed"
	gate_size_cap "$src_bytes" "$RELEASE_SIZE_CAP" ||
		die "size cap exceeded: $src_bytes > $RELEASE_SIZE_CAP"

	# (c) Free-space gate on the /output filesystem.
	local free
	free="$(free_bytes "$OUTPUT_ROOT")" || die "df of $OUTPUT_ROOT failed"
	gate_free_space "$src_bytes" "$FREE_HEADROOM_BYTES" "$free" ||
		die "insufficient free space on /output"

	# (d-pre) Reject path traversal / odd filenames before copying.
	scan_unsafe "$SOURCE_DIR" || die "unsafe filename(s) in source tree"

	# (d) Assemble into the new release dir; strip outside-pointing symlinks; chown.
	assemble "$SOURCE_DIR" "$RELEASE_ABS" "$PLATFORM_OWNER" ||
		die "assemble into $RELEASE_ABS failed"

	# (e) Post-assemble flip gate: re-measure and re-check the cap.
	local asm_bytes
	asm_bytes="$(du_bytes "$RELEASE_ABS")" || die "du of assembled release failed"
	gate_size_cap "$asm_bytes" "$RELEASE_SIZE_CAP" || {
		rm -rf -- "${RELEASE_ABS:?}"
		die "assembled size cap exceeded: $asm_bytes > $RELEASE_SIZE_CAP"
	}

	# (f) Atomic flip of current -> the new release (relative target).
	# Capture the OLD target first so the status delta can compare against it.
	local prev_release
	prev_release="$keep_current"
	atomic_flip "$OUTPUT_ROOT" "$RELEASE_REL" || die "atomic flip failed"

	# (g) Emit status JSON to the termination message (<4KB).
	emit_status "$asm_bytes" "$src_bytes" "$prev_release"
}

# emit_status <output_bytes> <source_bytes> <prev_release_basename>
emit_status() {
	local out_bytes="$1" src_bytes="$2" prev_release="$3"

	# Delta vs the previously-current release, if any (added/removed file counts).
	local prev_count=0 cur_count delta_added=0 delta_removed=0
	cur_count="$(find "$RELEASE_ABS" -type f 2>/dev/null | wc -l | tr -d ' ')"
	if [ -n "$prev_release" ] && [ -d "$RELEASES_DIR/$prev_release" ] &&
		[ "$prev_release" != "$RELEASE_TS" ]; then
		prev_count="$(find "$RELEASES_DIR/$prev_release" -type f 2>/dev/null | wc -l | tr -d ' ')"
	fi
	if [ "$cur_count" -ge "$prev_count" ]; then
		delta_added=$((cur_count - prev_count))
	else
		delta_removed=$((prev_count - cur_count))
	fi

	# du of the WHOLE releases dir, measured here (post retention sweep + flip) so
	# it reflects the on-PVC total across all retained releases. du_bytes pipes
	# du into awk, so its exit status is awk's (always 0) — a `|| total_bytes=0`
	# would never fire. Guard on the value instead: a non-integer/empty capture
	# (du failed) falls back to 0 so we never emit malformed JSON that would make
	# the operator discard the entire termination status (release flip + sizes).
	local total_bytes
	total_bytes="$(du_bytes "$RELEASES_DIR")"
	case "$total_bytes" in
	'' | *[!0-9]*) total_bytes=0 ;;
	esac

	# Field names match the operator's CopierMessage parser (internal/controller
	# /ensure.go): release.current flips the served-release pointer; sizes is the
	# per-volume du map the console renders -- sizes.output = the just-assembled
	# current release, sizes.outputTotal = every retained release on the output
	# PVC (releases dir, post-retention). sourceSize/outputSize are flat top-level
	# aliases for humans reading the raw termination log (source = build output on
	# the work volume; output = current release).
	printf '{"releaseTs":"%s","release":{"current":"%s"},"sizes":{"output":%s,"outputTotal":%s},"outputSize":%s,"sourceSize":%s,"deltas":{"prevFileCount":%s,"fileCount":%s,"filesAdded":%s,"filesRemoved":%s}}\n' \
		"$RELEASE_TS" "$RELEASE_TS" "$out_bytes" "$total_bytes" "$out_bytes" "$src_bytes" \
		"$prev_count" "$cur_count" "$delta_added" "$delta_removed" | head -c 4000 >"$TERM_LOG" 2>/dev/null ||
		printf 'copier: warning: could not write termination log\n' >&2

	printf 'copier: published release %s (%s bytes)\n' "$RELEASE_TS" "$out_bytes" >&2
}

# Only run main when executed, not when sourced by the tests.
if [ "${COPIER_LIB_ONLY:-0}" != "1" ]; then
	main "$@"
fi
