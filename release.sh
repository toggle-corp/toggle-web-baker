#!/bin/bash
# Release entrypoint — thin wrapper around fugit/scripts/release.sh.
# Bumps the chart version, regenerates CHANGELOG.md, and creates a signed tag.
#
#   ./release.sh            # prompts for the new version
#   ./release.sh 0.3.0      # pre-fills 0.3.0 in the prompt
#
# After it finishes: `git push origin <tag>` (fires the release workflow), then
# `git push` the release commit to the default branch.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export SCRIPT_DIR

# Runs before changelog generation. MUST `git add` anything it mutates, or the
# tag points at the un-bumped files (fugit only stages CHANGELOG.md itself).
function release_custom_hook {
    # Bare SemVer (VERSION_TAG_PREFIX_MODE=forbid); strip a leading `v`
    # defensively so Chart.yaml stays strict SemVer.
    # shellcheck disable=SC2154  # version_tag is assigned by fugit/scripts/release.sh before it calls this hook.
    chart_version="${version_tag#v}"
    chart="deploy/helm/toggle-web-baker/Chart.yaml"

    # version (chart) and appVersion (tool-image tags) move in lockstep with the
    # tag.
    sed -i.bak \
        -e "s/^version: .*/version: ${chart_version}/" \
        -e "s/^appVersion: .*/appVersion: \"${chart_version}\"/" \
        "$chart"
    rm -f "${chart}.bak"
    git add "$chart"

    # Node base images are versioned by a content hash of their Dockerfile, NOT
    # the release tag (see images/content-tag.sh): an unchanged node image keeps
    # its tag across releases so the cluster does not re-pull, and a changed one
    # gets a fresh tag so it does. CI publishes the exact same computed tag, so
    # pin it into values.yaml here. Anchored sed on the unique repository line
    # (rewrite the very next `tag:` line) preserves the file's comments and
    # formatting -- yq would reflow the whole file. Assert the write landed: a
    # silent sed no-op would otherwise ship a chart pointing at an absent tag.
    local values="deploy/helm/toggle-web-baker/values.yaml"
    local img tag repo got
    for img in node18 node24; do
        tag="$("$SCRIPT_DIR/images/content-tag.sh" "$img")"
        repo="ghcr.io/toggle-corp/toggle-web-baker-${img}"
        sed -i -E "\|repository: ${repo}\$|{n;s|(^[[:space:]]*tag:).*|\1 \"${tag}\"|}" "$values"
        got="$(grep -A1 -E "repository: ${repo}\$" "$values" \
            | grep -E '^[[:space:]]*tag:' | head -n1 \
            | sed -E 's/.*tag:[[:space:]]*"?([^"]*)"?.*/\1/')"
        if [ "$got" != "$tag" ]; then
            echo "release hook: failed to pin ${img} tag in ${values} (got [${got}], want [${tag}])" >&2
            exit 1
        fi
    done
    git add "$values"
}

export -f release_custom_hook
export START_COMMIT=2ead6462e492fd32cab3d9a56e287836dde49bef
export RELEASE_CUSTOM_HOOK=release_custom_hook
export REPO_NAME=toggle-corp/toggle-web-baker
export DEFAULT_BRANCH=main
# forbid => bare SemVer tags (0.3.0). Helm Chart.yaml version + OCI/image tags
# all reject a leading `v`.
export VERSION_TAG_PREFIX_MODE=forbid

export GIT_CLIFF__REMOTE__GITHUB__OWNER=toggle-corp
export GIT_CLIFF__REMOTE__GITHUB__REPO=toggle-web-baker

"$SCRIPT_DIR/fugit/scripts/release.sh" "${@:-}"
