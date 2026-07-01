#!/usr/bin/env bash
# clock_test.sh -- unit tests for the clock image entrypoint's ENV contract.
#
# Proves images/clock/entrypoint.sh consumes the environment-variable contract
# the operator produces (APP, REQUESTED_AT_ANNOTATION, BY_ANNOTATION) and emits
# the exact `kubectl annotate` the tick needs: set requested-at to a fresh epoch
# AND clear "by" in one --overwrite call. No container runtime: a stub `kubectl`
# is placed first on PATH so no cluster is needed.
#
#   bash images/test/clock_test.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

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

# assert_rc <expected_rc> <desc> -- check the rc captured in $rc.
assert_rc() {
	if [ "$rc" -eq "$1" ]; then ok "$2"; else no "$2 (rc=$rc, want $1)"; fi
}

# assert_contains <haystack> <needle> <desc>
assert_contains() {
	case "$1" in
	*"$2"*) ok "$3" ;;
	*) no "$3 (missing [$2] in [$1])" ;;
	esac
}

# assert_not_contains <haystack> <needle> <desc>
assert_not_contains() {
	case "$1" in
	*"$2"*) no "$3 (found [$2] in [$1])" ;;
	*) ok "$3" ;;
	esac
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# ---- stub kubectl -----------------------------------------------------------
# A fake `kubectl` that logs its full invocation to $KUBECTL_LOG and succeeds.
STUB_BIN="$TMP/bin"
mkdir -p "$STUB_BIN"
cat >"$STUB_BIN/kubectl" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$KUBECTL_LOG"
exit 0
STUB
chmod +x "$STUB_BIN/kubectl"
export PATH="$STUB_BIN:$PATH"

ENTRY="$HERE/../clock/entrypoint.sh"
KUBECTL_LOG="$TMP/kubectl.log"
export KUBECTL_LOG

RA="rebuild.baker.toggle-corp.com/requested-at"
BY="rebuild.baker.toggle-corp.com/by"

run_clock() {
	: >"$KUBECTL_LOG"
	rc=0
	err="$(bash "$ENTRY" 2>&1 1>/dev/null)" || rc=$?
}

# ---- 1. missing APP fails ---------------------------------------------------
unset APP REQUESTED_AT_ANNOTATION BY_ANNOTATION 2>/dev/null || true
export REQUESTED_AT_ANNOTATION="$RA" BY_ANNOTATION="$BY"
run_clock
assert_rc 1 "missing APP: exits 1"
assert_contains "$err" "APP" "missing APP: reports APP required"

# ---- 2. missing annotation keys fail ----------------------------------------
export APP=demo
unset REQUESTED_AT_ANNOTATION
export BY_ANNOTATION="$BY"
run_clock
assert_rc 1 "missing REQUESTED_AT_ANNOTATION: exits 1"

# ---- 3. valid env: exact annotate contract ----------------------------------
export APP=demo REQUESTED_AT_ANNOTATION="$RA" BY_ANNOTATION="$BY"
run_clock
assert_rc 0 "valid env: exits 0"
line="$(cat "$KUBECTL_LOG")"
assert_contains "$line" "annotate frontendapp demo" "targets the named FrontendApp"
assert_contains "$line" "${RA}=" "sets requested-at"
assert_contains "$line" "${BY}-" "CLEARS the by annotation (${BY}-)"
assert_contains "$line" "--overwrite" "uses --overwrite"
# requested-at value must be a fresh integer epoch (digits), never a literal.
ra_val="$(printf '%s\n' "$line" | grep -oE "${RA}=[0-9]+" | head -n1)"
assert_contains "$ra_val" "${RA}=" "requested-at value is numeric epoch"
assert_not_contains "$line" 'date +%s' "requested-at is EXPANDED, not a literal command"

# ---- summary ----------------------------------------------------------------
printf '\n# %s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
