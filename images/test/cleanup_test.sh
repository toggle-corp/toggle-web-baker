#!/usr/bin/env bash
# cleanup_test.sh -- unit tests for the cleanup image's MODE=releases pruning.
#
# Runs cleanup/entrypoint.sh end-to-end against a tmpdir RELEASES_DIR (no
# container runtime, no k8s API). MODE=cache (the default) is exercised by the
# operator/integration suites and needs a package manager, so it is not retested
# here; this file covers the NEW release-prune branch and its input validation.
#
#   bash images/test/cleanup_test.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENTRY="$HERE/../cleanup/entrypoint.sh"

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

# assert_eq <got> <want> <desc>
assert_eq() {
	if [ "$1" = "$2" ]; then ok "$3"; else no "$3 (got [$1], want [$2])"; fi
}
# assert_rc <expected_rc> <desc>
assert_rc() {
	if [ "$rc" -eq "$1" ]; then ok "$2"; else no "$2 (rc=$rc, want $1)"; fi
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# survivors <releases_dir> -- sorted, space-joined basenames of the dirs left.
survivors() {
	find "$1" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort | tr '\n' ' '
}

# ---- MODE=releases: keep newest KEEP_RELEASES + protected, prune the rest ----
# Five releases, lexically-sortable timestamps (the copier's naming scheme).
REL="$TMP/case1/releases"
mkdir -p "$REL"
for ts in 20260101T000000Z-1 20260102T000000Z-1 20260103T000000Z-1 \
	20260104T000000Z-1 20260105T000000Z-1; do
	mkdir -p "$REL/$ts"
	echo payload >"$REL/$ts/index.html"
done
# Keep the newest 2, and protect the OLD 20260101 (current/previous pointer).
rc=0
MODE=releases RELEASES_DIR="$REL" KEEP_RELEASES=2 \
	PROTECTED_RELEASES="20260101T000000Z-1" \
	TERMINATION_LOG="$TMP/case1/term.json" \
	bash "$ENTRY" || rc=$?
assert_rc 0 "releases: prune run exits 0"
# Newest 2 (04,05) survive; protected oldest (01) survives; 02,03 pruned.
assert_eq "$(survivors "$REL")" \
	"20260101T000000Z-1 20260104T000000Z-1 20260105T000000Z-1 " \
	"releases: keeps newest KEEP_RELEASES + protected, prunes rest"
blob="$(cat "$TMP/case1/term.json")"
case "$blob" in
	*'"action":"release-prune"'*) ok "releases: termination action is release-prune" ;;
	*) no "releases: wrong action ([$blob])" ;;
esac
case "$blob" in
	*'"kept":3'*) ok "releases: reports kept:3" ;;
	*) no "releases: wrong kept ([$blob])" ;;
esac
case "$blob" in
	*'"deleted":2'*) ok "releases: reports deleted:2" ;;
	*) no "releases: wrong deleted ([$blob])" ;;
esac
case "$blob" in
	*'"reclaimed":'*) ok "releases: reports reclaimed bytes" ;;
	*) no "releases: missing reclaimed ([$blob])" ;;
esac

# ---- MODE=releases: protected wins even when newer than KEEP would keep ------
REL2="$TMP/case2/releases"
mkdir -p "$REL2"
for ts in a b c; do mkdir -p "$REL2/$ts"; done
rc=0
MODE=releases RELEASES_DIR="$REL2" KEEP_RELEASES=1 \
	PROTECTED_RELEASES="a,b" \
	TERMINATION_LOG="$TMP/case2/term.json" \
	bash "$ENTRY" || rc=$?
assert_rc 0 "releases: comma-separated protected list run exits 0"
# keep newest 1 (c) + protected a,b -> all three survive.
assert_eq "$(survivors "$REL2")" "a b c " "releases: protected ids never deleted"

# ---- MODE=releases: empty dir -> deleted:0, exit 0 ---------------------------
REL3="$TMP/case3/releases"
mkdir -p "$REL3"
rc=0
MODE=releases RELEASES_DIR="$REL3" KEEP_RELEASES=2 \
	TERMINATION_LOG="$TMP/case3/term.json" \
	bash "$ENTRY" || rc=$?
assert_rc 0 "releases: empty dir exits 0"
blob="$(cat "$TMP/case3/term.json")"
case "$blob" in
	*'"deleted":0'*) ok "releases: empty dir reports deleted:0" ;;
	*) no "releases: empty dir wrong deleted ([$blob])" ;;
esac

# ---- MODE=releases: missing RELEASES_DIR is an input error -------------------
rc=0
MODE=releases KEEP_RELEASES=2 bash "$ENTRY" 2>/dev/null || rc=$?
assert_rc 1 "releases: missing RELEASES_DIR fails"

# ---- MODE=releases: missing KEEP_RELEASES is an input error ------------------
rc=0
MODE=releases RELEASES_DIR="$REL3" bash "$ENTRY" 2>/dev/null || rc=$?
assert_rc 1 "releases: missing KEEP_RELEASES fails"

# ---- MODE=releases: non-integer KEEP_RELEASES is an input error --------------
rc=0
MODE=releases RELEASES_DIR="$REL3" KEEP_RELEASES=two bash "$ENTRY" 2>/dev/null || rc=$?
assert_rc 1 "releases: non-integer KEEP_RELEASES fails"

# ---- MODE=releases: nonexistent RELEASES_DIR -> defensive deleted:0 ----------
rc=0
MODE=releases RELEASES_DIR="$TMP/case4/does-not-exist" KEEP_RELEASES=2 \
	TERMINATION_LOG="$TMP/case4-term.json" \
	bash "$ENTRY" || rc=$?
assert_rc 0 "releases: nonexistent dir exits 0 (defensive)"
blob="$(cat "$TMP/case4-term.json")"
case "$blob" in
	*'"deleted":0'*) ok "releases: nonexistent dir reports deleted:0" ;;
	*) no "releases: nonexistent dir wrong deleted ([$blob])" ;;
esac

# ---- unknown MODE is an input error ------------------------------------------
rc=0
MODE=bogus bash "$ENTRY" 2>/dev/null || rc=$?
assert_rc 1 "mode: unknown MODE fails"

# ---- summary -----------------------------------------------------------------
printf '\n# %s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
