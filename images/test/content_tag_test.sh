#!/usr/bin/env bash
# content_tag_test.sh -- unit test for content-tag.sh.
#
# Asserts the content-hash docker tag computed by content-tag.sh matches an
# independently-computed sha256 of the Dockerfile contents, is deterministic,
# is content-sensitive, and rejects bad input. Pure shell; needs NO docker.
#
#   bash images/test/content_tag_test.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/../content-tag.sh"
IMAGES="$HERE/.."

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
# assert_prefix <got> <want-prefix> <desc>
assert_prefix() {
	case "$1" in
	"$2"*) ok "$3" ;;
	*) no "$3 (got [$1], want prefix [$2])" ;;
	esac
}
# assert_ne <a> <b> <desc> -- fails if equal.
assert_ne() {
	if [ "$1" != "$2" ]; then ok "$3"; else no "$3 (both [$1])"; fi
}
# assert_fail <desc> -- $rc must be non-zero.
assert_fail() {
	if [ "$rc" -ne 0 ]; then ok "$1"; else no "$1 (expected non-zero rc, got 0)"; fi
}

# hash12 <file> -- first 12 hex chars of sha256 of the file's contents.
hash12() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -c1-12
	else
		shasum -a 256 "$1" | cut -c1-12
	fi
}

# ---- 1. tracer bullet: node18 -> "18-<hash>" --------------------------------
want18="18-$(hash12 "$IMAGES/node18/Dockerfile")"
got18="$(bash "$SCRIPT" node18)"
assert_eq "$got18" "$want18" "node18 tag equals independently-computed hash"

# ---- 2. node24 -> "24-<own hash>" -------------------------------------------
want24="24-$(hash12 "$IMAGES/node24/Dockerfile")"
got24="$(bash "$SCRIPT" node24)"
assert_prefix "$got24" "24-" "node24 tag begins with 24-"
assert_eq "$got24" "$want24" "node24 tag equals independently-computed hash"

# ---- 3. determinism ---------------------------------------------------------
a="$(bash "$SCRIPT" node18)"
b="$(bash "$SCRIPT" node18)"
assert_eq "$a" "$b" "two invocations give identical output"

# ---- 4. content-sensitivity -------------------------------------------------
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/node18"
cp "$IMAGES/node18/Dockerfile" "$tmp/node18/Dockerfile"
printf '#\n' >>"$tmp/node18/Dockerfile"
mutated="$(IMAGES_ROOT="$tmp" bash "$SCRIPT" node18)"
assert_ne "$mutated" "$got18" "appended byte changes the hash (hashes contents)"
rm -rf "$tmp"
trap - EXIT

# ---- 5. rejects bad input ---------------------------------------------------
rc=0
bash "$SCRIPT" operator >/dev/null 2>&1 || rc=$?
assert_fail "non-node name (operator) exits non-zero"

rc=0
bash "$SCRIPT" node99 >/dev/null 2>&1 || rc=$?
assert_fail "node99 (missing Dockerfile) exits non-zero"

# ---- summary ----------------------------------------------------------------
printf '\n# %s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
