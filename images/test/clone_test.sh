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

# assert_rc_var <expected_rc> <desc> -- check the rc captured in $arc (askpass).
assert_rc_var() {
	if [ "$arc" -eq "$1" ]; then ok "$2"; else no "$2 (rc=$arc, want $1)"; fi
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
#
# Fault injection (for the retry tests) via env, all scoped to `git clone`:
#   STUB_CLONE_FAIL_TIMES=N  -- fail the first N clone invocations (writing a
#       transient/network stderr class), then succeed. A shared counter file
#       ($STUB_CLONE_COUNT) survives across the retry loop's re-exec of `git`.
#   STUB_CLONE_TRANSIENT_MSG=<s> -- override the stderr the transient failures
#       emit (default: a "Could not resolve host" DNS blip). Used to assert an
#       ambiguous-but-retryable message (e.g. a 403 rate limit) is NOT
#       misclassified as permanent.
#   STUB_CLONE_FAIL_MSG=<s>  -- every clone invocation fails with <s> on stderr
#       (used to assert permanent-error fail-fast classification). Takes
#       precedence over STUB_CLONE_FAIL_TIMES.
STUB_BIN="$TMP/bin"
mkdir -p "$STUB_BIN"
cat >"$STUB_BIN/git" <<'STUB'
#!/usr/bin/env bash
# Log the full invocation, one line per call.
printf '%s\n' "$*" >>"$GIT_LOG"
case "$1" in
clone)
	# Snapshot the GIT_CONFIG_* env the clone runs with (the entrypoint's only
	# config channel under readOnlyRootFilesystem) so tests can assert on it.
	if [ -n "${ENV_LOG:-}" ]; then
		{
			i=0
			while [ "$i" -lt "${GIT_CONFIG_COUNT:-0}" ]; do
				eval "k=\${GIT_CONFIG_KEY_$i:-}"
				eval "v=\${GIT_CONFIG_VALUE_$i:-}"
				printf 'config %s=%s\n' "$k" "$v"
				i=$((i + 1))
			done
		} >>"$ENV_LOG"
	fi
	# Permanent-error injection: always fail with the given stderr message.
	if [ -n "${STUB_CLONE_FAIL_MSG:-}" ]; then
		printf '%s\n' "$STUB_CLONE_FAIL_MSG" >&2
		exit 128
	fi
	# Transient-error injection: fail the first N invocations, then succeed.
	if [ -n "${STUB_CLONE_FAIL_TIMES:-}" ] && [ -n "${STUB_CLONE_COUNT:-}" ]; then
		n=0
		[ -f "$STUB_CLONE_COUNT" ] && n="$(cat "$STUB_CLONE_COUNT")"
		n=$((n + 1))
		printf '%s' "$n" >"$STUB_CLONE_COUNT"
		if [ "$n" -le "$STUB_CLONE_FAIL_TIMES" ]; then
			printf '%s\n' "${STUB_CLONE_TRANSIENT_MSG:-fatal: unable to access: Could not resolve host: github.com}" >&2
			exit 128
		fi
	fi
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
ENV_LOG="$TMP/env.log"
export TERMINATION_LOG="$TERM_LOG" GIT_LOG ENV_LOG

run_clone() {
	: >"$TERM_LOG"
	: >"$GIT_LOG"
	: >"$ENV_LOG"
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

# ---- 6. SUBMODULES=1 fetches top-level submodules, NON-recursively ----------
# When the operator opts in, the clone must run a `git submodule update --init`
# pass WITHOUT --recursive (one level only, like actions/checkout
# submodules:true). Recursing would descend into nested SSH/private submodules
# the build pod can't reach and abort the whole clone.
unset SUBMODULES 2>/dev/null || true
export REPO="https://example/x" REF=main SRC_DIR="$TMP/src6" SUBMODULES=1
run_clone
assert_rc 0 "submodules on: exits 0"
sub_line="$(grep '^submodule update' "$GIT_LOG" | head -n1)"
if [ -n "$sub_line" ]; then
	ok "submodules on: git submodule update is invoked"
else
	no "submodules on: git submodule update is invoked (no 'submodule update' in git log)"
fi
case "$sub_line" in
*--recursive*) no "submodules on: submodule update is NON-recursive (found --recursive in [$sub_line])" ;;
*) ok "submodules on: submodule update is NON-recursive" ;;
esac
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

# ---- 8. no credential mounted -> SSH GitHub URLs rewritten to https ---------
# With nothing under the credential dir, the entrypoint must inject the
# url.https://github.com/.insteadOf rewrites (scp-style AND ssh://) so a public
# repo/submodule declared over SSH still clones anonymously.
export REPO="git@github.com:octo/repo.git" REF=main SRC_DIR="$TMP/src8"
export GIT_CREDENTIAL_DIR="$TMP/no-such-cred-dir"
run_clone
assert_rc 0 "no credential: exits 0"
env_seen="$(cat "$ENV_LOG")"
assert_contains "$env_seen" "url.https://github.com/.insteadOf=git@github.com:" \
	"no credential: scp-style SSH URL rewritten to https"
assert_contains "$env_seen" "url.https://github.com/.insteadOf=ssh://git@github.com/" \
	"no credential: ssh:// URL rewritten to https"

# ---- 9. credential mounted -> scp-style URL STILL rewritten to https ---------
# A token only works over https, so the ssh->https rewrite is UNCONDITIONAL: a
# mounted credential must NOT suppress it, or an scp-style repo URL would try
# ssh (no key, no https askpass) and fail auth. Same rewrite as the anonymous
# case; safe.directory stays KEY_0.
mkdir -p "$TMP/cred"
echo user >"$TMP/cred/username"
echo tok >"$TMP/cred/password"
export REPO="git@github.com:octo/repo.git" REF=main SRC_DIR="$TMP/src9" GIT_CREDENTIAL_DIR="$TMP/cred"
run_clone
assert_rc 0 "credential mounted: exits 0"
env_seen="$(cat "$ENV_LOG")"
assert_contains "$env_seen" "safe.directory=$TMP/src9" \
	"credential mounted: safe.directory still KEY_0"
assert_contains "$env_seen" "url.https://github.com/.insteadOf=git@github.com:" \
	"credential mounted: scp-style SSH URL still rewritten to https"
assert_contains "$env_seen" "url.https://github.com/.insteadOf=ssh://git@github.com/" \
	"credential mounted: ssh:// URL still rewritten to https"
unset GIT_CREDENTIAL_DIR

# ---- 10. transient clone failure is retried, then succeeds ------------------
# A DNS/network blip on pod start (the original "Could not resolve host" bug)
# must not fail the whole build: the per-op retry wraps `git clone`, so two
# transient failures followed by a success still exit 0 and clone to SRC_DIR.
# CLONE_RETRY_BASE_DELAY=0 keeps the test instant (no real backoff sleep).
unset SUBMODULES 2>/dev/null || true
export REPO="https://example/x" REF=main SRC_DIR="$TMP/src10"
export STUB_CLONE_FAIL_TIMES=2 STUB_CLONE_COUNT="$TMP/clone_count_10"
export CLONE_RETRY_BASE_DELAY=0
: >"$STUB_CLONE_COUNT"
run_clone
assert_rc 0 "transient clone: retried to success, exits 0"
clone_calls="$(grep -c '^clone ' "$GIT_LOG")"
assert_eq "$clone_calls" "3" "transient clone: git clone invoked 3x (2 fail + 1 success)"
unset STUB_CLONE_FAIL_TIMES STUB_CLONE_COUNT CLONE_RETRY_BASE_DELAY

# ---- 11. transient failure exhausts retries -> fails -------------------------
# With CLONE_RETRIES=3 and every attempt failing transiently, the entrypoint
# must exhaust exactly 3 attempts then fail (no infinite loop, no silent pass).
export REPO="https://example/x" REF=main SRC_DIR="$TMP/src11"
export STUB_CLONE_FAIL_TIMES=99 STUB_CLONE_COUNT="$TMP/clone_count_11"
export CLONE_RETRIES=3 CLONE_RETRY_BASE_DELAY=0
: >"$STUB_CLONE_COUNT"
run_clone
assert_rc 1 "exhausted clone: exits 1"
assert_contains "$err$(cat "$TERM_LOG")" "git clone" "exhausted clone: reports a clone failure"
clone_calls="$(grep -c '^clone ' "$GIT_LOG")"
assert_eq "$clone_calls" "3" "exhausted clone: attempted exactly CLONE_RETRIES=3 times"
unset STUB_CLONE_FAIL_TIMES STUB_CLONE_COUNT CLONE_RETRIES CLONE_RETRY_BASE_DELAY

# ---- 12. permanent error (auth) fails fast, NO retry ------------------------
# A real permission/not-found error must exit immediately — retrying a 15s
# backoff on a definitely-permanent failure just wastes the build deadline. The
# clone must be attempted exactly ONCE despite CLONE_RETRIES=3.
export REPO="https://example/x" REF=main SRC_DIR="$TMP/src12"
export STUB_CLONE_FAIL_MSG="fatal: Authentication failed for 'https://example/x'"
export CLONE_RETRIES=3 CLONE_RETRY_BASE_DELAY=0
run_clone
assert_rc 1 "permanent auth error: exits 1"
clone_calls="$(grep -c '^clone ' "$GIT_LOG")"
assert_eq "$clone_calls" "1" "permanent auth error: clone attempted exactly once (no retry)"
unset STUB_CLONE_FAIL_MSG CLONE_RETRIES CLONE_RETRY_BASE_DELAY

# ---- 13. permanent error (repository not found) fails fast, NO retry --------
export REPO="https://example/x" REF=main SRC_DIR="$TMP/src13"
export STUB_CLONE_FAIL_MSG="remote: Repository not found."
export CLONE_RETRIES=3 CLONE_RETRY_BASE_DELAY=0
run_clone
assert_rc 1 "permanent not-found error: exits 1"
clone_calls="$(grep -c '^clone ' "$GIT_LOG")"
assert_eq "$clone_calls" "1" "permanent not-found error: clone attempted exactly once (no retry)"
unset STUB_CLONE_FAIL_MSG CLONE_RETRIES CLONE_RETRY_BASE_DELAY

# ---- 14. transient 403 rate-limit is RETRIED, not failed-fast ---------------
# GitHub returns "403 Forbidden" for SECONDARY RATE LIMITS — very likely during
# a scheduled-build burst. The classifier must NOT treat a bare 403 as permanent
# (a real auth failure surfaces the specific "Authentication failed" string).
# One transient 403, then success => clone attempted twice, exits 0.
export REPO="https://example/x" REF=main SRC_DIR="$TMP/src14"
export STUB_CLONE_FAIL_TIMES=1 STUB_CLONE_COUNT="$TMP/cnt14"
export STUB_CLONE_TRANSIENT_MSG="fatal: unable to access 'https://github.com/x/': The requested URL returned error: 403 Forbidden"
export CLONE_RETRIES=3 CLONE_RETRY_BASE_DELAY=0
run_clone
assert_rc 0 "transient 403 rate-limit: retried to success, exits 0"
clone_calls="$(grep -c '^clone ' "$GIT_LOG")"
assert_eq "$clone_calls" "2" "transient 403 rate-limit: clone retried (1 fail + 1 success)"
unset STUB_CLONE_FAIL_TIMES STUB_CLONE_COUNT STUB_CLONE_TRANSIENT_MSG CLONE_RETRIES CLONE_RETRY_BASE_DELAY

# ---- git-askpass.sh: host-scoped credential answers (GIT_CREDENTIAL_HOST) ----
# The operator's host allowlist gates only spec.repo; a repo's .gitmodules can
# declare a submodule on any host and make git 401-prompt for it. The askpass
# helper must therefore answer ONLY prompts for GIT_CREDENTIAL_HOST when that
# var is set, and print NOTHING for any other host (fail-closed) — otherwise the
# operator-global credential would leak toward an unexpected submodule host.
ASKPASS="$HERE/../clone/git-askpass.sh"

# askpass <prompt> -- run the helper with the current env; captures rc + stdout.
# Credential dir points at $TMP/cred (username=user, password=tok, seeded above).
export GIT_CREDENTIAL_DIR="$TMP/cred"
askpass() {
	arc=0
	aout="$(bash "$ASKPASS" "$1")" || arc=$?
}

# scoped to github.com: a github.com prompt is answered with the credential.
export GIT_CREDENTIAL_HOST=github.com
askpass "Username for 'https://github.com': "
assert_rc_var 0 "askpass scoped: github.com Username exits 0"
assert_eq "$aout" "user" "askpass scoped: github.com Username answered"
askpass "Password for 'https://github.com': "
assert_eq "$aout" "tok" "askpass scoped: github.com Password answered"

# userinfo present (git re-prompts Password with the answered username in the
# URL) must still match on the bare hostname.
askpass "Password for 'https://x-access-token@github.com': "
assert_eq "$aout" "tok" "askpass scoped: userinfo form still answered"

# non-default port must match on hostname only (port ignored).
askpass "Username for 'https://github.com:8443': "
assert_eq "$aout" "user" "askpass scoped: non-default port still matches host"

# http:// scheme is parsed the same way.
askpass "Username for 'http://github.com': "
assert_eq "$aout" "user" "askpass scoped: http:// scheme matches host"

# WRONG host (the .gitmodules attack): print NOTHING, exit 0 — fail closed.
askpass "Username for 'https://evil.example/x.git': "
assert_rc_var 0 "askpass wrong host: exits 0"
assert_eq "$aout" "" "askpass wrong host: prints NOTHING (no credential leak)"
askpass "Password for 'https://x-access-token@evil.example/x.git': "
assert_eq "$aout" "" "askpass wrong host: userinfo form on other host also blank"

# unset GIT_CREDENTIAL_HOST -> old behavior: answer ANY host.
unset GIT_CREDENTIAL_HOST
askpass "Username for 'https://evil.example/x.git': "
assert_eq "$aout" "user" "askpass unset: answers any host (back-compat)"
askpass "Password for 'https://github.com': "
assert_eq "$aout" "tok" "askpass unset: answers github.com too"

# a non-Username/Password prompt is ignored regardless of host scoping.
export GIT_CREDENTIAL_HOST=github.com
askpass "Some other prompt for 'https://github.com': "
assert_rc_var 0 "askpass other prompt: exits 0"
assert_eq "$aout" "" "askpass other prompt: prints nothing"
unset GIT_CREDENTIAL_HOST GIT_CREDENTIAL_DIR

# ---- git-askpass.sh parity: clone and clock helpers stay identical -----------
# The pair carries cross-reference comments and has drifted in comments before; a
# silent FUNCTIONAL divergence would break exactly one of clone/watch. Assert the
# two files are identical after stripping comment-only lines (leading # lines).
strip_comments() { grep -vE '^[[:space:]]*#' "$1"; }
if diff <(strip_comments "$HERE/../clone/git-askpass.sh") \
	<(strip_comments "$HERE/../clock/git-askpass.sh") >/dev/null 2>&1; then
	ok "askpass parity: clone and clock helpers identical (sans comments)"
else
	no "askpass parity: clone and clock helpers identical (sans comments)"
fi

# ---- summary ----------------------------------------------------------------
printf '\n# %s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
