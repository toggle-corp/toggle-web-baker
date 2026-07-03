#!/usr/bin/env bash
# copier_test.sh -- unit tests for the copier gate/assembly logic.
#
# Sources copier/lib.sh (the same code the entrypoint uses) and exercises each
# gate against a real tmpdir. No container runtime required.
#
#   bash images/test/copier_test.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=../copier/lib.sh
. "$HERE/../copier/lib.sh"

PASS=0
FAIL=0

ok() {
	PASS=$((PASS + 1))
	printf 'ok   - %s\n' "$1"
}
no() {
	FAIL=$((FAIL + 1))
	printf 'FAIL - %s\n' "$1"
}

# assert_rc <expected_rc> <desc> -- run the LAST command's rc captured in $rc.
assert_rc() {
	if [ "$rc" -eq "$1" ]; then ok "$2"; else no "$2 (rc=$rc, want $1)"; fi
}

# assert_eq <got> <want> <desc>
assert_eq() {
	if [ "$1" = "$2" ]; then ok "$3"; else no "$3 (got [$1], want [$2])"; fi
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# ---- size cap gate ----------------------------------------------------------
rc=0
gate_size_cap 100 200 || rc=$?
assert_rc 0 "size gate: under cap passes"

rc=0
gate_size_cap 300 200 || rc=$?
assert_rc 1 "size gate: over cap rejects"

rc=0
gate_size_cap 999999 0 || rc=$?
assert_rc 0 "size gate: cap 0 means unset (always passes)"

rc=0
gate_size_cap abc 200 || rc=$?
assert_rc 2 "size gate: non-numeric source is an input error"

# ---- free-space gate --------------------------------------------------------
rc=0
gate_free_space 100 50 200 || rc=$?
assert_rc 0 "free gate: source+headroom <= free passes"

rc=0
gate_free_space 100 150 200 || rc=$?
assert_rc 1 "free gate: source+headroom > free rejects"

rc=0
gate_free_space 200 0 200 || rc=$?
assert_rc 0 "free gate: exact fit passes"

# ---- safe_name / scan_unsafe ------------------------------------------------
rc=0
safe_name "index.html" || rc=$?
assert_rc 0 "safe_name: ordinary file"
rc=0
safe_name ".." || rc=$?
assert_rc 1 "safe_name: .. rejected"
rc=0
safe_name "-MywkTgq81K7yQnbEMgr" || rc=$?
assert_rc 0 "safe_name: leading-dash slug allowed (Firebase/Next route ids; call sites use --)"
rc=0
safe_name "a/b" || rc=$?
assert_rc 1 "safe_name: path separator rejected"

SRC="$TMP/src"
mkdir -p "$SRC/assets"
echo hi >"$SRC/index.html"
echo body >"$SRC/assets/app.js"
rc=0
scan_unsafe "$SRC" || rc=$?
assert_rc 0 "scan_unsafe: clean tree passes"

# ---- assemble: strip outside-pointing symlink, keep inside ones, chown -------
# Put a symlink pointing OUTSIDE the tree (to /etc/passwd) and one INSIDE.
ln -s /etc/passwd "$SRC/evil-link"
ln -s index.html "$SRC/good-link"
DEST="$TMP/out/releases/r1"
rc=0
# chown to our own uid:gid so it succeeds without root.
assemble "$SRC" "$DEST" "$(id -u):$(id -g)" || rc=$?
assert_rc 0 "assemble: rsync --safe-links + chown succeeds"

if [ ! -e "$DEST/evil-link" ]; then
	ok "assemble: outside-pointing symlink stripped"
else
	no "assemble: outside-pointing symlink leaked into release"
fi
if [ -L "$DEST/good-link" ]; then
	ok "assemble: inside-pointing symlink preserved"
else
	no "assemble: inside-pointing symlink dropped"
fi
if [ -f "$DEST/index.html" ] && [ -f "$DEST/assets/app.js" ]; then
	ok "assemble: regular files copied"
else
	no "assemble: regular files missing"
fi

# ---- atomic flip ------------------------------------------------------------
OUT="$TMP/out"
rc=0
atomic_flip "$OUT" "releases/r1" || rc=$?
assert_rc 0 "atomic_flip: creates current symlink"
if [ "$(readlink "$OUT/current")" = "releases/r1" ]; then
	ok "atomic_flip: current points at the new release (relative)"
else
	no "atomic_flip: current target wrong"
fi
if [ ! -e "$OUT/current.tmp" ]; then
	ok "atomic_flip: no leftover current.tmp"
else
	no "atomic_flip: current.tmp not consumed by rename"
fi
# Re-flip to a second release must overwrite atomically.
mkdir -p "$OUT/releases/r2"
rc=0
atomic_flip "$OUT" "releases/r2" || rc=$?
assert_rc 0 "atomic_flip: re-flip succeeds"
assert_eq "$(readlink "$OUT/current")" "releases/r2" "atomic_flip: re-flip repoints current"

# ---- retention sweep --------------------------------------------------------
SW="$TMP/sweep/releases"
mkdir -p "$SW"
for ts in 20260101T000000Z 20260102T000000Z 20260103T000000Z 20260104T000000Z; do
	mkdir -p "$SW/$ts"
done
# current points at the oldest; keep current + newest 1.
ln -sfn "releases/20260101T000000Z" "$TMP/sweep/current"
rc=0
retention_sweep "$SW" 1 "20260101T000000Z" || rc=$?
assert_rc 0 "retention: sweep runs"
# Expect kept: 20260101 (current) + 20260104 (newest). Removed: 02, 03.
remaining="$(find "$SW" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort | tr '\n' ' ')"
if [ "$remaining" = "20260101T000000Z 20260104T000000Z " ]; then
	ok "retention: keeps current + newest KEEP_RELEASES, prunes rest"
else
	no "retention: wrong survivors: [$remaining]"
fi

# keep=0, no current -> everything pruned.
SW2="$TMP/sweep2/releases"
mkdir -p "$SW2/a" "$SW2/b"
rc=0
retention_sweep "$SW2" 0 "" || rc=$?
assert_rc 0 "retention: keep=0 no-current runs"
left="$(find "$SW2" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')"
assert_eq "$left" "0" "retention: keep=0 prunes all"

# ---- phase-env reader -------------------------------------------------------
PE="$TMP/phase-env"
printf 'BAR=baz\nFOO="2026-06-25T00:00:00Z"\n' >"$PE"
val="$(read_phase_env_value "$PE" FOO)"
assert_eq "$val" "2026-06-25T00:00:00Z" "phase-env: reads FOO (quotes stripped)"
val="$(read_phase_env_value "$PE" MISSING)"
assert_eq "$val" "" "phase-env: missing key -> empty"
val="$(read_phase_env_value "$TMP/nope" FOO)"
assert_eq "$val" "" "phase-env: missing file -> empty"

# ---- emit_status termination JSON -------------------------------------------
# Source the entrypoint (lib-only, main suppressed) to get emit_status, then
# assert the blob carries the fields the operator's CopierMessage parser reads:
# release.current (pointer flip) and the sizes map (storage bars). The config
# block runs at source time, so OUTPUT_DIR is required and TERMINATION_LOG /
# PHASE_ENV must be pointed at the tmpdir BEFORE sourcing. Exported so the
# sourced emit_status sees them (and so shellcheck counts them as used).
# Two release dirs so outputTotal (du of the WHOLE releases dir) sums both and
# is distinguishable from the current release's own size.
mkdir -p "$TMP/emit/releases/r-new" "$TMP/emit/releases/r-old"
: >"$TMP/emit/releases/r-new/index.html"
: >"$TMP/emit/releases/r-old/index.html"
export OUTPUT_DIR=out
export TERMINATION_LOG="$TMP/emit/term.json"
export PHASE_ENV="$TMP/nope-phase-env" # missing -> empty freshness
# Point OUTPUT_ROOT at the tmp tree so RELEASES_DIR (derived at source time)
# is the releases dir we just populated, and outputTotal measures it.
export OUTPUT_ROOT="$TMP/emit"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=../copier/entrypoint.sh
COPIER_LIB_ONLY=1 . "$HERE/../copier/entrypoint.sh"
export RELEASE_ABS="$TMP/emit/releases/r-new"
export RELEASE_TS="20260630T120000Z-1"
emit_status 4096 8192 "" || no "emit_status: returned non-zero"
blob="$(cat "$TMP/emit/term.json")"
case "$blob" in
	*'"release":{"current":"20260630T120000Z-1"}'*) ok "emit_status: emits release.current for the pointer flip" ;;
	*) no "emit_status: missing release.current ([$blob])" ;;
esac
# sizes.outputTotal = du of the WHOLE releases dir (both r-new + r-old).
emit_total="$(du_bytes "$TMP/emit/releases")"
case "$blob" in
	*"\"outputTotal\":$emit_total"*) ok "emit_status: sizes.outputTotal = du of the releases dir" ;;
	*) no "emit_status: missing/wrong sizes.outputTotal (want $emit_total, [$blob])" ;;
esac
case "$blob" in
	*'"sizes":{"output":4096,"outputTotal":'*) ok "emit_status: sizes map is {output,outputTotal}" ;;
	*) no "emit_status: wrong sizes map shape ([$blob])" ;;
esac
case "$blob" in
	*'"source":'*) no "emit_status: sizes must not contain a source key ([$blob])" ;;
	*) ok "emit_status: sizes no longer carries source" ;;
esac
# releaseCount = the number of retained release dirs (r-new + r-old = 2), the
# REAL on-disk count behind the console's "Output (N releases)" label.
case "$blob" in
	*'"releaseCount":2'*) ok "emit_status: releaseCount counts the retained release dirs" ;;
	*) no "emit_status: missing/wrong releaseCount (want 2, [$blob])" ;;
esac
case "$blob" in
	*'"sourceSize":8192'*) ok "emit_status: sourceSize flat alias present" ;;
	*) no "emit_status: missing sourceSize flat alias ([$blob])" ;;
esac
case "$blob" in
	*'"outputSize":4096'*) ok "emit_status: outputSize flat alias present" ;;
	*) no "emit_status: missing outputSize flat alias ([$blob])" ;;
esac

# ---- summary ----------------------------------------------------------------
printf '\n# %s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
