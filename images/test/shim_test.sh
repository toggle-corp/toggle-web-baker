#!/usr/bin/env bash
# Unit tests for the shim wrapper binary (images/shim). Builds the Go binary
# once into a temp dir, then exercises wrap/install semantics against a FAKE
# cgroup root (SHIM_CGROUP_ROOT) and termination log (TERMINATION_LOG) — no
# container runtime needed. Mirrors the harness style of the sibling tests.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

SHIM="${TMP}/shim"
(cd "${HERE}/../shim" && CGO_ENABLED=0 go build -o "${SHIM}" .)

fails=0
check() { # check <name> <condition...>
  local name="$1"; shift
  if "$@"; then echo "  ok: ${name}"; else echo "  FAIL: ${name}"; fails=$((fails + 1)); fi
}

# Fake cgroup v2 root with a known peak.
CG="${TMP}/cg"
mkdir -p "${CG}"
echo "634040320" > "${CG}/memory.peak"

echo "== wrap: success path writes peak and preserves exit 0"
LOG="${TMP}/term1"
SHIM_CGROUP_ROOT="${CG}" TERMINATION_LOG="${LOG}" "${SHIM}" -- sh -c 'echo hello' > "${TMP}/out1"
check "exit 0 propagated" test $? -eq 0
check "child stdout inherited" grep -q hello "${TMP}/out1"
check "peak written" grep -q '^peakMemoryBytes=634040320$' "${LOG}"

echo "== wrap: child failure code propagates, peak still written"
LOG="${TMP}/term2"
rc=0
SHIM_CGROUP_ROOT="${CG}" TERMINATION_LOG="${LOG}" "${SHIM}" -- sh -c 'exit 3' || rc=$?
check "exit 3 propagated" test "${rc}" -eq 3
check "peak written on failure" grep -q '^peakMemoryBytes=634040320$' "${LOG}"

echo "== wrap: signal-killed child reads as 128+sig (OOM SIGKILL => 137)"
LOG="${TMP}/term3"
rc=0
SHIM_CGROUP_ROOT="${CG}" TERMINATION_LOG="${LOG}" "${SHIM}" -- sh -c 'kill -9 $$' || rc=$?
check "137 for SIGKILL" test "${rc}" -eq 137
check "peak written after kill" grep -q '^peakMemoryBytes=634040320$' "${LOG}"

echo "== wrap: command not found => 127, no crash"
rc=0
SHIM_CGROUP_ROOT="${CG}" TERMINATION_LOG="${TMP}/term4" "${SHIM}" -- /no/such/binary || rc=$?
check "127 for unstartable command" test "${rc}" -eq 127

echo "== wrap: cgroup v1 fallback path"
CG1="${TMP}/cg1"
mkdir -p "${CG1}/memory"
echo "111" > "${CG1}/memory/memory.max_usage_in_bytes"
LOG="${TMP}/term5"
SHIM_CGROUP_ROOT="${CG1}" TERMINATION_LOG="${LOG}" "${SHIM}" -- true
check "v1 peak written" grep -q '^peakMemoryBytes=111$' "${LOG}"

echo "== wrap: unreadable peak is a silent no-op (outcome unchanged)"
LOG="${TMP}/term6"
SHIM_CGROUP_ROOT="${TMP}/nonexistent" TERMINATION_LOG="${LOG}" "${SHIM}" -- true
check "exit 0 despite missing cgroup" test $? -eq 0
check "no log written" test ! -s "${LOG}"

echo "== install: copies self, executable, atomic"
DEST="${TMP}/installed-shim"
"${SHIM}" install "${DEST}"
check "installed binary exists" test -x "${DEST}"
SHIM_CGROUP_ROOT="${CG}" TERMINATION_LOG="${TMP}/term7" "${DEST}" -- true
check "installed binary works" grep -q '^peakMemoryBytes=634040320$' "${TMP}/term7"
check "no tmp remnant" test ! -e "${DEST}.tmp"

echo
if [ "${fails}" -gt 0 ]; then echo "shim_test: ${fails} FAILURES"; exit 1; fi
echo "shim_test: all checks passed"
