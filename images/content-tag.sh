#!/usr/bin/env bash
# content-tag.sh -- compute the content-hash docker tag for a node base image.
#
# Emits "<major>-<12hex>" where <major> is the node major version parsed from the
# image name (node18 -> 18) and <12hex> is the first 12 hex chars of the sha256
# of the image's Dockerfile CONTENTS. Because it hashes contents (not the path),
# rebuilding an unchanged Dockerfile yields the same tag -- so release.sh and CI
# agree on the tag without coordinating, and unchanged images skip rebuilds.
#
# Only node images (/^node[0-9]+$/) are content-tagged; other images use a
# different scheme and are rejected here.
#
#   bash images/content-tag.sh node18   # -> 18-a1b2c3d4e5f6
#
# IMAGES_ROOT overrides where <image-name>/Dockerfile is resolved (defaults to
# the dir holding this script, i.e. images/); used by tests to point at fixtures.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGES_ROOT="${IMAGES_ROOT:-$HERE}"

name="${1:-}"
if ! [[ "$name" =~ ^node[0-9]+$ ]]; then
	printf 'content-tag: image name must match /^node[0-9]+$/ (got [%s])\n' "$name" >&2
	exit 1
fi

major="${name#node}"
dockerfile="$IMAGES_ROOT/$name/Dockerfile"
if [ ! -f "$dockerfile" ]; then
	printf 'content-tag: no Dockerfile at %s\n' "$dockerfile" >&2
	exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
	hex="$(sha256sum "$dockerfile")"
else
	hex="$(shasum -a 256 "$dockerfile")"
fi
hex="${hex%% *}"

printf '%s-%s\n' "$major" "${hex:0:12}"
