#!/usr/bin/env bash
# clone_test.sh -- unit tests for the clone image entrypoint's ENV contract.
#
# Proves images/clone/entrypoint.sh consumes the environment-variable contract
# the operator produces (REPO, REF, SRC_DIR), guarding against the original
# `clone: REPO is required` bug. No container runtime: a stub `git` is placed
# first on PATH so no network or real git is needed.
#
#   bash images/test/clone_test.sh
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

# assert_eq <got> <want> <desc>
assert_eq() {
	if [ "$1" = "$2" ]; then ok "$3"; else no "$3 (got [$1], want [$2])"; fi
}

# assert_contains <haystack> <needle> <desc>
assert_contains() {
	case "$1" in
	*"$2"*) ok "$3" ;;
	*) no "$3 (missing [$2] in [$1])" ;;
	esac
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# ---- stub git ---------------------------------------------------------------
# A fake `git` that logs every invocation to $GIT_LOG and succeeds. For
# `git clone ... -- <repo> <dest>` it mkdir's the dest (the LAST arg) so the
# entrypoint's subsequent `cd "$SRC_DIR"` works; `git rev-parse HEAD` echoes a
# fake sha; everything else (config/fetch/checkout/submodule) exits 0.
STUB_BIN="$TMP/bin"
mkdir -p "$STUB_BIN"
cat >"$STUB_BIN/git" <<'STUB'
#!/usr/bin/env bash
# Log the full invocation, one line per call.
printf '%s\n' "$*" >>"$GIT_LOG"
case "$1" in
clone)
	# Destination is the last positional argument.
	dest="${!#}"
	mkdir -p "$dest"
	exit 0
	;;
rev-parse)
	echo "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	exit 0
	;;
*)
	exit 0
	;;
esac
STUB
chmod +x "$STUB_BIN/git"
export PATH="$STUB_BIN:$PATH"

ENTRY="$HERE/../clone/entrypoint.sh"

# run_clone -- run the entrypoint with a fresh termination log + git log under a
# clean env (only the vars we export here are passed through), capturing rc.
# Reads test-local REPO/REF/DEPTH/SRC_DIR from the shell's exported env.
TERM_LOG="$TMP/term.log"
GIT_LOG="$TMP/git.log"
export TERMINATION_LOG="$TERM_LOG" GIT_LOG

run_clone() {
	: >"$TERM_LOG"
	: >"$GIT_LOG"
	rc=0
	err="$(bash "$ENTRY" 2>&1 1>/dev/null)" || rc=$?
}

# ---- 1. missing REPO fails (THE original bug) -------------------------------
unset REPO REF DEPTH SRC_DIR 2>/dev/null || true
export REF=main SRC_DIR="$TMP/src1"
run_clone
assert_rc 1 "missing REPO: exits 1"
assert_contains "$err$(cat "$TERM_LOG")" "REPO is required" "missing REPO: reports 'REPO is required'"

# ---- 2. missing REF fails ---------------------------------------------------
unset REF
export REPO="https://example/x" SRC_DIR="$TMP/src2"
run_clone
assert_rc 1 "missing REF: exits 1"
assert_contains "$err$(cat "$TERM_LOG")" "REF is required" "missing REF: reports 'REF is required'"

# ---- 3. valid env succeeds + clones to SRC_DIR (the contract) ---------------
export REPO="https://example/x" REF=main SRC_DIR="$TMP/src3"
run_clone
assert_rc 0 "valid env: exits 0"
clone_line="$(grep '^clone ' "$GIT_LOG" | head -n1)"
# The entrypoint must pass SRC_DIR as the clone destination (the LAST argument).
assert_eq "${clone_line##* }" "$TMP/src3" "valid env: git clone destination is SRC_DIR"

# ---- 4. non-empty SRC_DIR is refused ----------------------------------------
mkdir -p "$TMP/src4"
echo occupied >"$TMP/src4/existing"
export REPO="https://example/x" REF=main SRC_DIR="$TMP/src4"
run_clone
assert_rc 1 "non-empty SRC_DIR: exits 1"
assert_contains "$err$(cat "$TERM_LOG")" "already exists" "non-empty SRC_DIR: reports 'already exists'"

# ---- 5. non-integer DEPTH rejected ------------------------------------------
export REPO="https://example/x" REF=main SRC_DIR="$TMP/src5" DEPTH=abc
run_clone
assert_rc 1 "bad DEPTH: exits 1"
assert_contains "$err$(cat "$TERM_LOG")" "DEPTH must be a positive integer" "bad DEPTH: reports 'DEPTH must be a positive integer'"
unset DEPTH

# ---- 6. SUBMODULES=1 recurses submodules ------------------------------------
# When the operator opts in, the clone must use --recurse-submodules AND run a
# `git submodule update` pass.
unset SUBMODULES 2>/dev/null || true
export REPO="https://example/x" REF=main SRC_DIR="$TMP/src6" SUBMODULES=1
run_clone
assert_rc 0 "submodules on: exits 0"
clone_line="$(grep '^clone ' "$GIT_LOG" | head -n1)"
assert_contains "$clone_line" "--recurse-submodules" "submodules on: git clone includes --recurse-submodules"
if grep -q '^submodule update' "$GIT_LOG"; then
	ok "submodules on: git submodule update is invoked"
else
	no "submodules on: git submodule update is invoked (no 'submodule update' in git log)"
fi
unset SUBMODULES

# ---- 7. default (no SUBMODULES) skips submodules ----------------------------
# Default off: no --recurse-submodules on the clone, and NO submodule update.
unset SUBMODULES 2>/dev/null || true
export REPO="https://example/x" REF=main SRC_DIR="$TMP/src7"
run_clone
assert_rc 0 "submodules off: exits 0"
clone_line="$(grep '^clone ' "$GIT_LOG" | head -n1)"
case "$clone_line" in
*--recurse-submodules*) no "submodules off: git clone omits --recurse-submodules (found it in [$clone_line])" ;;
*) ok "submodules off: git clone omits --recurse-submodules" ;;
esac
if grep -q '^submodule update' "$GIT_LOG"; then
	no "submodules off: git submodule update is NOT invoked (found 'submodule update' in git log)"
else
	ok "submodules off: git submodule update is NOT invoked"
fi

# ---- summary ----------------------------------------------------------------
printf '\n# %s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
