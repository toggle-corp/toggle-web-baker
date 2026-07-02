#!/usr/bin/env bash
# check-images.sh -- drift-guard for the platform's docker image set.
#
# The set of images the platform ships is declared in four places that must
# stay in lockstep:
#   * on disk        -- one Dockerfile per image (the SOURCE OF TRUTH here)
#   * CI matrix      -- .github/workflows/ci.yml docker_build strategy
#   * release body   -- the `for img in ...` loop in release.yml
#   * chart values   -- ghcr.io/toggle-corp/toggle-web-baker-<name> repositories
#
# This derives the canonical set from disk, then asserts each of the three
# consumers lists EXACTLY that set. Any drift prints a per-consumer diff to
# stderr and exits 1; all-match prints an ok line and exits 0.
#
#   bash images/check-images.sh
#
# Inputs come from env vars (defaults point at the real repo) so tests can aim
# the check at fixtures:
#   REPO_ROOT    dir holding Dockerfile, console/Dockerfile, images/*/Dockerfile
#   CI_YML       path to ci.yml
#   RELEASE_YML  path to release.yml
#   VALUES_YAML  path to the chart values.yaml
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="${REPO_ROOT:-$(cd "$HERE/.." && pwd)}"
CI_YML="${CI_YML:-$REPO_ROOT/.github/workflows/ci.yml}"
RELEASE_YML="${RELEASE_YML:-$REPO_ROOT/.github/workflows/release.yml}"
VALUES_YAML="${VALUES_YAML:-$REPO_ROOT/deploy/helm/toggle-web-baker/values.yaml}"

# newline-joined, unique-sorted list of the given words.
sort_uniq() {
	printf '%s\n' "$@" | sort -u
}

# ---- canonical set (from disk) ---------------------------------------------
canonical_set() {
	local names=()
	[ -f "$REPO_ROOT/Dockerfile" ] && names+=("operator")
	[ -f "$REPO_ROOT/console/Dockerfile" ] && names+=("console")
	local d name
	for d in "$REPO_ROOT"/images/*/; do
		name="$(basename "$d")"
		[ "$name" = "test" ] && continue
		[ -f "$d/Dockerfile" ] && names+=("$name")
	done
	sort_uniq "${names[@]}"
}

# ---- consumer sets ----------------------------------------------------------
ci_matrix_set() {
	yq '.jobs.docker_build.strategy.matrix.include[].name' "$CI_YML" | sort -u
}

release_body_set() {
	# Grab the words between `in` and `;` on the `for img in ... ; do` line.
	local line
	line="$(grep -E 'for img in .* ; do|for img in .*; do' "$RELEASE_YML" | head -n1)"
	line="${line#*for img in }"
	line="${line%%;*}"
	# Split on whitespace WITHOUT glob-expanding the words (a stray `*` in the
	# list would otherwise expand against the cwd).
	local -a words
	read -ra words <<<"$line"
	sort_uniq "${words[@]}"
}

values_set() {
	# Hyphen is in the class so a future hyphenated image name (e.g. node-18)
	# isn't truncated. OCI repo names are lowercase, so no uppercase needed.
	grep -oE 'toggle-web-baker-[a-z0-9-]+' "$VALUES_YAML" |
		sed 's/^toggle-web-baker-//' | sort -u
}

# ---- diff one consumer against canonical ------------------------------------
# report_diff <consumer-label> <canonical-list> <consumer-list>
# Prints missing/extra lines to stderr; returns 1 on any diff.
report_diff() {
	local label="$1" canon="$2" got="$3" missing extra rc=0
	missing="$(comm -23 <(printf '%s\n' "$canon") <(printf '%s\n' "$got"))"
	extra="$(comm -13 <(printf '%s\n' "$canon") <(printf '%s\n' "$got"))"
	if [ -n "$missing" ]; then
		printf 'DRIFT [%s]: missing (on disk but not in %s): %s\n' \
			"$label" "$label" "$(echo "$missing" | tr '\n' ' ')" >&2
		rc=1
	fi
	if [ -n "$extra" ]; then
		printf 'DRIFT [%s]: extra (in %s but not on disk): %s\n' \
			"$label" "$label" "$(echo "$extra" | tr '\n' ' ')" >&2
		rc=1
	fi
	return "$rc"
}

main() {
	local canon ci rel val rc=0
	canon="$(canonical_set)"
	ci="$(ci_matrix_set)"
	rel="$(release_body_set)"
	val="$(values_set)"

	report_diff "ci-matrix" "$canon" "$ci" || rc=1
	report_diff "release-body" "$canon" "$rel" || rc=1
	report_diff "values" "$canon" "$val" || rc=1

	if [ "$rc" -ne 0 ]; then
		printf '\ncanonical (from disk): %s\n' "$(echo "$canon" | tr '\n' ' ')" >&2
		exit 1
	fi

	printf 'ok - image set consistent across disk, ci-matrix, release-body, values: %s\n' \
		"$(echo "$canon" | tr '\n' ' ')"
}

main "$@"
