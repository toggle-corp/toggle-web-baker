#!/usr/bin/env bash
# du-entrypoint.sh -- measure one PVC and report its byte count.
#
# Mounts the target read-only at /target. Writes the integer apparent size in
# bytes (du -sb) to the termination message (<=4KB). Nothing else.
set -euo pipefail

TERM_LOG="${TERMINATION_LOG:-/dev/termination-log}"
TARGET="${TARGET:-/target}"

if [ ! -d "$TARGET" ]; then
	printf '%s\n' "du: target $TARGET not found" | head -c 4000 >"$TERM_LOG" 2>/dev/null || true
	printf '%s\n' "du: target $TARGET not found" >&2
	exit 1
fi

bytes="$(du -sb -- "$TARGET" | awk '{print $1}')"

printf '%s' "$bytes" >"$TERM_LOG"
printf 'du: %s -> %s bytes\n' "$TARGET" "$bytes" >&2
