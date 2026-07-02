#!/usr/bin/env bash
# check_images_test.sh -- tests for check-images.sh (the image drift-guard).
#
# No docker needed: the checker only reads text files. Fixtures are temp copies
# of the real ci.yml / release.yml / values.yaml with one image removed/added,
# aimed at the checker via its CI_YML / RELEASE_YML / VALUES_YAML / REPO_ROOT
# env vars.
#
#   bash images/test/check_images_test.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
CHECK="$REPO/images/check-images.sh"

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

TMP="$(mktemp -d)"
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT

# ---- 1. tracer: real repo files, all three consumers match -----------------
rc=0
out="$(bash "$CHECK" 2>&1)" || rc=$?
if [ "$rc" -eq 0 ]; then ok "real repo: exit 0"; else no "real repo: exit 0 (rc=$rc, out=$out)"; fi
case "$out" in
*"consistent across disk, ci-matrix, release-body, values"*) ok "real repo: reports all consumers match" ;;
*) no "real repo: reports all consumers match (out=$out)" ;;
esac

# ---- 2. missing-in-values: drop the ...-du repository lines ----------------
vals_no_du="$TMP/values-no-du.yaml"
grep -v 'toggle-web-baker-du' "$REPO/deploy/helm/toggle-web-baker/values.yaml" >"$vals_no_du"
rc=0
out="$(VALUES_YAML="$vals_no_du" bash "$CHECK" 2>&1)" || rc=$?
if [ "$rc" -ne 0 ]; then ok "missing-in-values: exit non-zero"; else no "missing-in-values: exit non-zero (rc=0)"; fi
case "$out" in *"du"*) ok "missing-in-values: mentions du" ;; *) no "missing-in-values: mentions du (out=$out)" ;; esac

# ---- 3. missing-in-matrix: drop the { name: clock, ... } matrix row --------
ci_no_clock="$TMP/ci-no-clock.yml"
grep -v 'name: clock,' "$REPO/.github/workflows/ci.yml" >"$ci_no_clock"
rc=0
out="$(CI_YML="$ci_no_clock" bash "$CHECK" 2>&1)" || rc=$?
if [ "$rc" -ne 0 ]; then ok "missing-in-matrix: exit non-zero"; else no "missing-in-matrix: exit non-zero (rc=0)"; fi
case "$out" in *"clock"*) ok "missing-in-matrix: mentions clock" ;; *) no "missing-in-matrix: mentions clock (out=$out)" ;; esac

# ---- 4. missing-in-release-body: drop node24 from the for-loop list --------
rel_no_node24="$TMP/release-no-node24.yml"
sed 's/for img in operator console clone copier du cleanup clock node18 node24;/for img in operator console clone copier du cleanup clock node18;/' \
	"$REPO/.github/workflows/release.yml" >"$rel_no_node24"
rc=0
out="$(RELEASE_YML="$rel_no_node24" bash "$CHECK" 2>&1)" || rc=$?
if [ "$rc" -ne 0 ]; then ok "missing-in-release-body: exit non-zero"; else no "missing-in-release-body: exit non-zero (rc=0)"; fi
case "$out" in *"node24"*) ok "missing-in-release-body: mentions node24" ;; *) no "missing-in-release-body: mentions node24 (out=$out)" ;; esac

# ---- 5. extra-on-disk: add images/foo/Dockerfile in a temp REPO_ROOT -------
# Build a fake repo root: symlink the real consumer files (.github, deploy) and
# the real Dockerfiles/dirs, then add an unconsumed images/foo image.
fake="$TMP/fakerepo"
mkdir -p "$fake/images" "$fake/console"
ln -s "$REPO/.github" "$fake/.github"
ln -s "$REPO/deploy" "$fake/deploy"
ln -s "$REPO/Dockerfile" "$fake/Dockerfile"
ln -s "$REPO/console/Dockerfile" "$fake/console/Dockerfile"
for d in "$REPO"/images/*/; do
	name="$(basename "$d")"
	[ -f "$d/Dockerfile" ] || continue
	mkdir -p "$fake/images/$name"
	ln -s "$d/Dockerfile" "$fake/images/$name/Dockerfile"
done
mkdir -p "$fake/images/foo"
: >"$fake/images/foo/Dockerfile"
rc=0
out="$(REPO_ROOT="$fake" bash "$CHECK" 2>&1)" || rc=$?
if [ "$rc" -ne 0 ]; then ok "extra-on-disk: exit non-zero"; else no "extra-on-disk: exit non-zero (rc=0)"; fi
case "$out" in *"foo"*) ok "extra-on-disk: mentions foo" ;; *) no "extra-on-disk: mentions foo (out=$out)" ;; esac

# ---- summary ----------------------------------------------------------------
printf '\n# %s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
