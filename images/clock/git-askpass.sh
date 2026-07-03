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
# Host scoping (GIT_CREDENTIAL_HOST, operator-injected at mount time):
#   The operator's host allowlist only gates spec.repo. But a repo's .gitmodules
#   is content-controlled by repo committers, not the platform: it can declare a
#   submodule at https://evil.example/x.git, and git would then 401-prompt for
#   evil.example — handing our operator-global credential to an unexpected host.
#   So when GIT_CREDENTIAL_HOST is set (lowercase hostname, e.g. github.com) we
#   answer ONLY prompts whose quoted URL is for exactly that host; for any other
#   host we print NOTHING and exit 0 (git proceeds anonymously and fails loudly
#   on a private/rate-limited host — fail-closed, the credential never leaves
#   toward an unexpected host). Unset/empty preserves the old answer-any-prompt
#   behavior for manual/back-compat use.
#
# Host match is on HOSTNAME ONLY, ignoring any port: git prompts per-host and the
# credential is a host identity (a token for github.com is the same secret on
# :443 or a non-default :8443), so comparing hostnames — not host:port — is the
# correct scoping and avoids a spurious mismatch on an explicit-port URL.
#
# The credential value is NEVER echoed in any diagnostic path; no set -x.
#
# This is kept textually in sync with images/clone/git-askpass.sh (a parity
# guard in images/test asserts the two are identical after stripping comments):
# one feature, two mount points (this watcher AND the clone pod).
set -euo pipefail

cred_dir="${GIT_CREDENTIAL_DIR:-/run/git-credential}"

case "$1" in
	Username*) f="${cred_dir}/username" ;;
	Password*) f="${cred_dir}/password" ;;
	*)         exit 0 ;;
esac

# Host scoping: when GIT_CREDENTIAL_HOST is set, answer only for that host.
if [ -n "${GIT_CREDENTIAL_HOST:-}" ]; then
	# Extract the quoted URL from the prompt: "... for 'URL': " -> "URL': "
	# then drop the trailing "': " (and anything after the closing quote).
	rest="${1#*\'}"
	url="${rest%%\'*}"
	# Strip scheme (https:// or http://) -> authority + optional /path.
	authority="${url#*://}"
	# Drop any /path, ?query, or #fragment -> bare authority (userinfo@host:port).
	authority="${authority%%/*}"
	# Strip userinfo (e.g. x-access-token@) via the LAST '@' on the authority.
	hostport="${authority##*@}"
	# Match on hostname only: drop any :port (see header comment for why).
	host="${hostport%%:*}"
	# Fail closed: any host other than the scoped one gets NO credential.
	if [ "$host" != "$GIT_CREDENTIAL_HOST" ]; then
		exit 0
	fi
fi

# Print the secret only to git's stdin pipe. Never persist it.
if [ -r "$f" ]; then
	cat -- "$f"
fi
