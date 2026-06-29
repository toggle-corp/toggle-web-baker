#!/usr/bin/env bash
# clone-entrypoint.sh -- anonymous (or optionally credentialed) source checkout.
#
# Phase 1 of the build pipeline (first initContainer). Clones $REPO at $REF into
# /workspace/src, optionally recursing submodules ($SUBMODULES). Shallow if
# $DEPTH is set.
#
# Pods have automountServiceAccountToken:false, so we return nothing via the k8s
# API. Failures simply exit non-zero with a short reason written to the
# termination-message file so the operator can surface it.
#
# Env in:
#   REPO        (required)  -- clone URL, expected public.
#   REF         (required)  -- branch, tag, or full commit sha to check out.
#   DEPTH       (optional)  -- positive integer; if set, shallow clone to that depth.
#   SUBMODULES  (optional)  -- when 1/true, recurse submodules; default off.
#
# Security notes:
#   * GIT_ASKPASS points at a helper that reads an OPTIONAL credential mount and
#     never writes it to the shared work volume. We also disable the on-disk
#     credential store and prompting so git can never persist a token under
#     /workspace where later phases (setup/fetch/build/copy) could read it.
#   * No .git-credentials, no `git config --global credential.helper store`.
set -euo pipefail

TERM_LOG="${TERMINATION_LOG:-/dev/termination-log}"
SRC_DIR="${SRC_DIR:-/workspace/src}"

# fail "<reason>" -- write a short reason to the termination log and exit 1.
fail() {
	# Keep the message small (<4KB); the operator reads it verbatim.
	printf '%s\n' "clone: $1" | head -c 4000 >"$TERM_LOG" 2>/dev/null || true
	printf '%s\n' "clone: $1" >&2
	exit 1
}

[ -n "${REPO:-}" ] || fail "REPO is required"
[ -n "${REF:-}" ] || fail "REF is required"

# Refuse to clone over an existing non-empty target so a re-run on a reused work
# volume can't silently merge two checkouts.
if [ -e "$SRC_DIR" ] && [ -n "$(ls -A "$SRC_DIR" 2>/dev/null || true)" ]; then
	fail "target $SRC_DIR already exists and is non-empty"
fi

# Never prompt; never fall back to an interactive terminal; never persist creds.
export GIT_TERMINAL_PROMPT=0
export GIT_ASKPASS=/usr/local/bin/git-askpass.sh
# Defensive: clear any inherited credential helper for this process tree.
git config --global --unset-all credential.helper 2>/dev/null || true
git config --global credential.helper "" 2>/dev/null || true

# Mark the checkout dir safe. $SRC_DIR is a root-owned emptyDir mount, but the
# build pod runs us as a non-root UID, so git's CVE-2022-24765 "dubious
# ownership" guard would abort every command after the initial clone (fetch,
# checkout, submodule) with a fatal we then surface only as a generic checkout
# failure. Inject via GIT_CONFIG_* env so it needs no writable HOME/.gitconfig
# under the pod's readOnlyRootFilesystem.
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0=safe.directory
export GIT_CONFIG_VALUE_0="$SRC_DIR"

# SUBMODULES opt-in: only recurse submodules when explicitly enabled (1/true).
# Default off, so an app without submodules: true never fetches submodules.
submodules=0
case "${SUBMODULES:-}" in
1 | [Tt][Rr][Uu][Ee]) submodules=1 ;;
esac

clone_args=""
if [ "$submodules" = 1 ]; then
	clone_args="--recurse-submodules --shallow-submodules"
fi
if [ -n "${DEPTH:-}" ]; then
	case "$DEPTH" in
	'' | *[!0-9]*) fail "DEPTH must be a positive integer, got '$DEPTH'" ;;
	esac
	clone_args="$clone_args --depth $DEPTH"
fi

# Clone, then checkout the requested ref. We clone the default branch first
# (works for both shallow and full) then fetch+checkout $REF so tags, branches,
# and raw commit shas are all handled uniformly.
# shellcheck disable=SC2086  # clone_args is an intentional word-split arg list.
git clone $clone_args -- "$REPO" "$SRC_DIR" || fail "git clone of $REPO failed"

cd "$SRC_DIR" || fail "cannot enter $SRC_DIR"

# Fetch the specific ref (handles the case where $REF is not the default branch).
if [ -n "${DEPTH:-}" ]; then
	git fetch --depth "$DEPTH" origin "$REF" 2>/dev/null || true
else
	git fetch origin "$REF" 2>/dev/null || true
fi

checkout_args="--force"
if [ "$submodules" = 1 ]; then
	checkout_args="--recurse-submodules --force"
fi
# shellcheck disable=SC2086  # checkout_args is an intentional word-split arg list.
git checkout $checkout_args "$REF" 2>/dev/null ||
	git checkout --force FETCH_HEAD 2>/dev/null ||
	fail "cannot checkout ref '$REF'"

if [ "$submodules" = 1 ]; then
	git submodule update --init --recursive ${DEPTH:+--depth "$DEPTH"} ||
		fail "submodule update failed"
fi

resolved="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
printf 'clone: checked out %s at %s\n' "$REF" "$resolved" >&2
