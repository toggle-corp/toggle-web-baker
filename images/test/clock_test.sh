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
# `kubectl get` answers with $LAST_SEEN_STUB (the watcher's jsonpath read of the
# last-seen annotation) so watch-mode state is scriptable per test.
STUB_BIN="$TMP/bin"
mkdir -p "$STUB_BIN"
cat >"$STUB_BIN/kubectl" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$KUBECTL_LOG"
if [ "${1:-}" = "get" ]; then printf '%s' "${LAST_SEEN_STUB:-}"; fi
exit 0
STUB
chmod +x "$STUB_BIN/kubectl"

# ---- stub git ----------------------------------------------------------------
# A fake `git` that logs to $GIT_LOG; `ls-remote` prints $LS_REMOTE_STUB and
# exits $LS_REMOTE_RC (default 0) so remote SHAs and failures are scriptable.
# For ls-remote it also snapshots GIT_ASKPASS and the GIT_CONFIG_* env (the
# watcher's only config channel under readOnlyRootFilesystem) to $ENV_LOG so
# tests can assert the credential + rewrite wiring the same way clone_test does.
cat >"$STUB_BIN/git" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$GIT_LOG"
if [ "${1:-}" = "ls-remote" ]; then
	if [ -n "${ENV_LOG:-}" ]; then
		{
			printf 'askpass=%s\n' "${GIT_ASKPASS:-}"
			i=0
			while [ "$i" -lt "${GIT_CONFIG_COUNT:-0}" ]; do
				eval "k=\${GIT_CONFIG_KEY_$i:-}"
				eval "v=\${GIT_CONFIG_VALUE_$i:-}"
				printf 'config %s=%s\n' "$k" "$v"
				i=$((i + 1))
			done
		} >>"$ENV_LOG"
	fi
	printf '%s\n' "${LS_REMOTE_STUB:-}"
	exit "${LS_REMOTE_RC:-0}"
fi
exit 0
STUB
chmod +x "$STUB_BIN/git"
export PATH="$STUB_BIN:$PATH"

ENTRY="$HERE/../clock/entrypoint.sh"
KUBECTL_LOG="$TMP/kubectl.log"
GIT_LOG="$TMP/git.log"
ENV_LOG="$TMP/env.log"
export KUBECTL_LOG GIT_LOG ENV_LOG

RA="rebuild.baker.toggle-corp.com/requested-at"
BY="rebuild.baker.toggle-corp.com/by"
CM="rebuild.baker.toggle-corp.com/commit"

run_clock() {
	: >"$KUBECTL_LOG"
	: >"$GIT_LOG"
	: >"$ENV_LOG"
	rc=0
	err="$(bash "$ENTRY" 2>&1 1>/dev/null)" || rc=$?
}

RES="apps.baker.toggle-corp.com"

# ---- 1. missing APP fails ---------------------------------------------------
unset APP RESOURCE REQUESTED_AT_ANNOTATION BY_ANNOTATION COMMIT_ANNOTATION 2>/dev/null || true
export RESOURCE="$RES" REQUESTED_AT_ANNOTATION="$RA" BY_ANNOTATION="$BY" COMMIT_ANNOTATION="$CM"
run_clock
assert_rc 1 "missing APP: exits 1"
assert_contains "$err" "APP" "missing APP: reports APP required"

# ---- 2. missing resource / annotation keys fail -------------------------------
export APP=demo
unset RESOURCE
run_clock
assert_rc 1 "missing RESOURCE: exits 1"

export RESOURCE="$RES"
unset REQUESTED_AT_ANNOTATION
export BY_ANNOTATION="$BY" COMMIT_ANNOTATION="$CM"
run_clock
assert_rc 1 "missing REQUESTED_AT_ANNOTATION: exits 1"

export REQUESTED_AT_ANNOTATION="$RA"
unset COMMIT_ANNOTATION
run_clock
assert_rc 1 "missing COMMIT_ANNOTATION: exits 1"

# ---- 3. valid env: exact annotate contract ----------------------------------
export APP=demo RESOURCE="$RES" REQUESTED_AT_ANNOTATION="$RA" BY_ANNOTATION="$BY" COMMIT_ANNOTATION="$CM"
run_clock
assert_rc 0 "valid env: exits 0"
line="$(cat "$KUBECTL_LOG")"
assert_contains "$line" "annotate apps.baker.toggle-corp.com demo" "targets the named App"
assert_contains "$line" "${RA}=" "sets requested-at"
assert_contains "$line" "${BY}-" "CLEARS the by annotation (${BY}-)"
assert_contains "$line" "${CM}-" "CLEARS the commit annotation (${CM}-)"
assert_contains "$line" "--overwrite" "uses --overwrite"
# requested-at value must be a fresh integer epoch (digits), never a literal.
ra_val="$(printf '%s\n' "$line" | grep -oE "${RA}=[0-9]+" | head -n1)"
assert_contains "$ra_val" "${RA}=" "requested-at value is numeric epoch"
assert_not_contains "$line" 'date +%s' "requested-at is EXPANDED, not a literal command"

# ---- 4. watch mode: first tick seeds last-seen, does NOT trigger -------------
LS="watch.baker.toggle-corp.com/last-seen-sha"
SHA_A="1111111111111111111111111111111111111111"
export MODE=watch REPO="https://github.com/acme/site.git" REF="main" \
	LAST_SEEN_ANNOTATION="$LS"
export LS_REMOTE_STUB="$SHA_A	refs/heads/main"
export LAST_SEEN_STUB=""
run_clock
assert_rc 0 "watch seed: exits 0"
gitline="$(cat "$GIT_LOG")"
assert_contains "$gitline" "ls-remote https://github.com/acme/site.git main" "watch seed: ls-remote repo+ref"
annotates="$(grep '^annotate' "$KUBECTL_LOG" || true)"
assert_contains "$annotates" "${LS}=${SHA_A}" "watch seed: records last-seen"
assert_not_contains "$annotates" "${RA}=" "watch seed: does NOT set requested-at"
assert_not_contains "$annotates" "${CM}=" "watch seed: does NOT set commit"

# ---- 5. watch mode: unchanged SHA is a no-op ---------------------------------
export LAST_SEEN_STUB="$SHA_A"
run_clock
assert_rc 0 "watch same SHA: exits 0"
annotates="$(grep '^annotate' "$KUBECTL_LOG" || true)"
if [ -z "$annotates" ]; then ok "watch same SHA: no annotate at all"; else no "watch same SHA: no annotate at all (got [$annotates])"; fi

# ---- 6. watch mode: new SHA triggers atomically -------------------------------
SHA_B="2222222222222222222222222222222222222222"
export LS_REMOTE_STUB="$SHA_B	refs/heads/main"
export LAST_SEEN_STUB="$SHA_A"
run_clock
assert_rc 0 "watch new SHA: exits 0"
annotates="$(grep '^annotate' "$KUBECTL_LOG" || true)"
n_annotates="$(grep -c '^annotate' "$KUBECTL_LOG" || true)"
if [ "$n_annotates" -eq 1 ]; then ok "watch new SHA: exactly ONE annotate call"; else no "watch new SHA: exactly ONE annotate call (got $n_annotates)"; fi
assert_contains "$annotates" "${RA}=" "watch new SHA: sets requested-at"
assert_contains "$annotates" "${CM}=${SHA_B}" "watch new SHA: sets commit SHA"
assert_contains "$annotates" "${LS}=${SHA_B}" "watch new SHA: advances last-seen"
assert_contains "$annotates" "${BY}-" "watch new SHA: clears by"
assert_contains "$annotates" "--overwrite" "watch new SHA: uses --overwrite"

# ---- 7. watch mode: ls-remote failure patches nothing --------------------------
export LS_REMOTE_RC=128
run_clock
if [ "$rc" -ne 0 ]; then ok "watch ls-remote failure: exits nonzero"; else no "watch ls-remote failure: exits nonzero (rc=0)"; fi
annotates="$(grep '^annotate' "$KUBECTL_LOG" || true)"
if [ -z "$annotates" ]; then ok "watch ls-remote failure: patches nothing"; else no "watch ls-remote failure: patches nothing (got [$annotates])"; fi
unset LS_REMOTE_RC

# ---- 7b. watch mode: exact ref wins over tail-matched lookalikes ----------------
# ls-remote patterns tail-match path components: asking for "main" also returns
# refs/heads/feature/main and refs/tags/main, sorted BEFORE refs/heads/main.
# The watcher must select the exact branch, not the first line.
SHA_C="3333333333333333333333333333333333333333"
export LS_REMOTE_STUB="$SHA_B	refs/heads/feature/main
$SHA_C	refs/heads/main
$SHA_A	refs/tags/main"
export LAST_SEEN_STUB="$SHA_A"
run_clock
assert_rc 0 "watch exact ref: exits 0"
annotates="$(grep '^annotate' "$KUBECTL_LOG" || true)"
assert_contains "$annotates" "${CM}=${SHA_C}" "watch exact ref: picks refs/heads/main, not feature/main or the tag"

# a tag is selected when no branch matches
export LS_REMOTE_STUB="$SHA_A	refs/tags/main"
export LAST_SEEN_STUB="$SHA_A"
run_clock
assert_rc 0 "watch tag fallback: exits 0"
annotates="$(grep '^annotate' "$KUBECTL_LOG" || true)"
if [ -z "$annotates" ]; then ok "watch tag fallback: matching tag SHA is a no-op"; else no "watch tag fallback: matching tag SHA is a no-op (got [$annotates])"; fi

# ---- 8. watch mode: empty REF defaults to HEAD ---------------------------------
unset REF
export LS_REMOTE_STUB="$SHA_A	HEAD"
export LAST_SEEN_STUB="$SHA_A"
run_clock
assert_rc 0 "watch default ref: exits 0"
gitline="$(cat "$GIT_LOG")"
assert_contains "$gitline" "ls-remote https://github.com/acme/site.git HEAD" "watch default ref: ls-remote uses HEAD"

# ---- 9. watch mode: GIT_ASKPASS wired for the ls-remote poll --------------------
# The watcher must run ls-remote with GIT_ASKPASS pointed at the shared helper so
# an operator-mounted credential (GIT_CREDENTIAL_DIR/{username,password}) answers
# GitHub's https auth prompt and lifts the anonymous rate limit — one feature,
# two mount points (this watcher AND the clone pod).
export REF="main"
export LS_REMOTE_STUB="$SHA_A	refs/heads/main"
export LAST_SEEN_STUB="$SHA_A"
run_clock
assert_rc 0 "watch askpass: exits 0"
env_seen="$(cat "$ENV_LOG")"
assert_contains "$env_seen" "askpass=/usr/local/bin/git-askpass.sh" \
	"watch askpass: GIT_ASKPASS points at the shared helper"

# ---- 10. watch mode: ssh->https GitHub rewrite is UNCONDITIONAL -----------------
# A token only works over https and the pod has no ssh key, so an scp-style or
# ssh:// GitHub REPO is ALWAYS rewritten to https, mounted credential or not.
# With a credential mounted:
export REF="main"
export LS_REMOTE_STUB="$SHA_A	refs/heads/main"
export LAST_SEEN_STUB="$SHA_A"
mkdir -p "$TMP/cred"
echo user >"$TMP/cred/username"
echo tok >"$TMP/cred/password"
export REPO="git@github.com:acme/site.git" GIT_CREDENTIAL_DIR="$TMP/cred"
run_clock
assert_rc 0 "watch rewrite (cred): exits 0"
env_seen="$(cat "$ENV_LOG")"
assert_contains "$env_seen" "url.https://github.com/.insteadOf=git@github.com:" \
	"watch rewrite (cred): scp-style SSH URL rewritten to https"
assert_contains "$env_seen" "url.https://github.com/.insteadOf=ssh://git@github.com/" \
	"watch rewrite (cred): ssh:// URL rewritten to https"
unset GIT_CREDENTIAL_DIR
# And with NO credential mounted the rewrite still applies:
export REPO="git@github.com:acme/site.git" GIT_CREDENTIAL_DIR="$TMP/no-such-cred-dir"
run_clock
assert_rc 0 "watch rewrite (anon): exits 0"
env_seen="$(cat "$ENV_LOG")"
assert_contains "$env_seen" "url.https://github.com/.insteadOf=git@github.com:" \
	"watch rewrite (anon): scp-style SSH URL rewritten to https"
assert_contains "$env_seen" "url.https://github.com/.insteadOf=ssh://git@github.com/" \
	"watch rewrite (anon): ssh:// URL rewritten to https"
unset GIT_CREDENTIAL_DIR
export REPO="https://github.com/acme/site.git"

# ---- 11. git-askpass.sh: host-scoped credential answers (GIT_CREDENTIAL_HOST) -
# Same fail-closed host scoping as the clone helper (the pair is kept identical):
# with GIT_CREDENTIAL_HOST set, answer ONLY prompts for that host; print NOTHING
# for any other host so a .gitmodules-declared submodule on evil.example never
# receives the operator-global credential. Unset preserves answer-any behavior.
ASKPASS="$HERE/../clock/git-askpass.sh"

# $TMP/cred (username=user, password=tok) was seeded above in test 10.
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

# ---- summary ----------------------------------------------------------------
printf '\n# %s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
