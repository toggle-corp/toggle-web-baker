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
    chart_version="${version_tag#v}"
    chart="deploy/helm/toggle-web-baker/Chart.yaml"

    # version (chart) and appVersion (image tags) move in lockstep with the tag.
    sed -i.bak \
        -e "s/^version: .*/version: ${chart_version}/" \
        -e "s/^appVersion: .*/appVersion: \"${chart_version}\"/" \
        "$chart"
    rm -f "${chart}.bak"

    git add "$chart"
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
