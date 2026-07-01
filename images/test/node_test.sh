#!/usr/bin/env bash
# node_test.sh -- runtime smoke test for the node18 base image.
#
# Builds (or reuses) the node18 image and asserts, by RUNNING it, that the
# platform contract holds: node 18, and the tools the phase entrypoints need
# (bash, git, corepack + the yarn/pnpm shims) are present, and the baked user is
# UID 1000. Needs a container runtime; if docker is unavailable the test SKIPS
# cleanly (exit 0) rather than failing, so `make test` stays green off-cluster.
#
#   bash images/test/node_test.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CTX="$HERE/../node18"
IMG="${IMG:-toggle-web-baker-node18:test}"

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

# assert_prefix <got> <want-prefix> <desc>
assert_prefix() {
	case "$1" in
	"$2"*) ok "$3" ;;
	*) no "$3 (got [$1], want prefix [$2])" ;;
	esac
}
# assert_eq <got> <want> <desc>
assert_eq() {
	if [ "$1" = "$2" ]; then ok "$3"; else no "$3 (got [$1], want [$2])"; fi
}
# assert_ok <desc> -- $rc must be 0.
assert_ok() {
	if [ "$rc" -eq 0 ]; then ok "$1"; else no "$1 (rc=$rc)"; fi
}

# ---- docker guard -----------------------------------------------------------
# No runtime -> skip (exit 0). Mirrors the platform convention that unit gates
# stay green without a daemon.
if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
	printf 'SKIP - docker unavailable; node18 runtime smoke test skipped\n'
	exit 0
fi

# ---- build ------------------------------------------------------------------
docker build -t "$IMG" "$CTX" >/dev/null

# run <cmd...> -- run the image, capture stdout in $out and rc in $rc.
run() {
	rc=0
	out="$(docker run --rm "$IMG" "$@" 2>/dev/null)" || rc=$?
}

# runrt <cmd...> -- run under the OPERATOR's runtime contract: readOnlyRootFilesystem
# with only the writable emptyDir mounts (work, tmp) and HOME=/work. This is what
# actually breaks corepack if a package manager isn't pre-activated into the baked
# COREPACK_HOME (it would try to download into the read-only root). A plain `run`
# (default HOME=/home/node, writable) hides that failure, so the PM checks below
# MUST use runrt.
runrt() {
	rc=0
	out="$(docker run --rm --read-only --tmpfs /work --tmpfs /tmp -e HOME=/work "$IMG" "$@" 2>/dev/null)" || rc=$?
}

# ---- node is v18 ------------------------------------------------------------
run node --version
assert_ok "node --version runs"
assert_prefix "$out" "v18" "node is v18"

# ---- bash works -------------------------------------------------------------
run bash --version
assert_ok "bash --version runs"

# ---- git works --------------------------------------------------------------
run git --version
assert_ok "git --version runs"

# ---- corepack works ---------------------------------------------------------
run corepack --version
assert_ok "corepack --version runs"

# ---- yarn + pnpm resolve OFFLINE under the operator's runtime contract ------
# (read-only rootfs + HOME=/work). Regression guard: without pre-activating both
# managers into the baked COREPACK_HOME, corepack tries to download at runtime
# and fails (EROFS, or fetches a node-incompatible "latest" that crashes).
runrt yarn --version
assert_ok "yarn --version runs read-only under HOME=/work"
runrt pnpm --version
assert_ok "pnpm --version runs read-only under HOME=/work"

# ---- baked user is UID 1000 -------------------------------------------------
run id -u
assert_ok "id -u runs"
assert_eq "$out" "1000" "baked user is UID 1000"

# ---- summary ----------------------------------------------------------------
printf '\n# %s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
