#!/usr/bin/env bash
# git-askpass.sh -- GIT_ASKPASS helper for the clone phase.
#
# git invokes this with the prompt text as $1, e.g.
#   "Username for 'https://github.com': "  or  "Password for '...': ".
# We answer from a credential file mounted by the operator (read-only, for a
# FUTURE private-repo case), NEVER persisting anything to the shared work
# volume. If no credential is mounted we print nothing, so anonymous clone of a
# public repo proceeds unchanged.
#
# Credential mount convention (operator-controlled, optional):
#   /run/git-credential/username
#   /run/git-credential/password   (or a PAT)
#
# This is byte-identical in intent to images/clock/git-askpass.sh: one feature,
# two mount points (the clone pod AND the commit watcher). Keep the two in sync.
set -euo pipefail

cred_dir="${GIT_CREDENTIAL_DIR:-/run/git-credential}"

case "$1" in
	Username*) f="${cred_dir}/username" ;;
	Password*) f="${cred_dir}/password" ;;
	*)         exit 0 ;;
esac

# Print the secret only to git's stdin pipe. Never write it to /workspace.
if [ -r "$f" ]; then
	cat -- "$f"
fi
