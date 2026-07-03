#!/usr/bin/env bash
# git-askpass.sh -- GIT_ASKPASS helper for the clock watch mode (ls-remote).
#
# git invokes this with the prompt text as $1, e.g.
#   "Username for 'https://github.com': "  or  "Password for '...': ".
# We answer from a credential file mounted by the operator (read-only), NEVER
# persisting anything. If no credential is mounted we print nothing, so an
# anonymous `git ls-remote` of a public repo proceeds unchanged.
#
# Credential mount convention (operator-controlled, optional):
#   /run/git-credential/username
#   /run/git-credential/password   (or a PAT)
#
# This is byte-identical in intent to images/clone/git-askpass.sh: one feature,
# two mount points (this watcher AND the clone pod). Keep the two in sync.
set -euo pipefail

cred_dir="${GIT_CREDENTIAL_DIR:-/run/git-credential}"

case "$1" in
	Username*) f="${cred_dir}/username" ;;
	Password*) f="${cred_dir}/password" ;;
	*)         exit 0 ;;
esac

# Print the secret only to git's stdin pipe. Never persist it.
if [ -r "$f" ]; then
	cat -- "$f"
fi
