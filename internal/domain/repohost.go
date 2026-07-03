package domain

import (
	"fmt"
	"net/url"
	"strings"
)

// RepoHost extracts the lowercase hostname from a git repo URL. It exists for a
// security decision, not cosmetics: the operator-global git credential is a real
// secret, and spec.repo is user-controlled. Injecting the credential
// unconditionally would let an attacker set repo to https://evil.com/x.git and
// harvest the token via the git askpass helper (a credential-forwarding leak).
// The operator therefore compares the repo's host against an allowlist BEFORE
// forwarding the credential, and that comparison needs one canonical host string
// regardless of which of git's URL grammars the user wrote.
//
// It normalizes all three real-world forms to a bare lowercase hostname,
// dropping any userinfo (git@) and port (only the hostname is compared):
//   - https://github.com/org/repo(.git)  -> github.com
//   - git@github.com:org/repo.git (scp)  -> github.com
//   - ssh://git@github.com/org/repo      -> github.com
//
// An empty or unparseable repo, or one with no host, is an error. Callers treat
// that as a no-match and fall back to anonymous access (fail-closed): a repo we
// can't confidently place on a known host never receives the credential.
func RepoHost(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", fmt.Errorf("repo URL is empty")
	}

	// scp-style syntax "git@host:path" has no scheme and uses a colon (not a
	// slash) to separate host from path. Detect it before url.Parse, which does
	// not understand this form. A leading "scheme://" (contains "://") is a real
	// URL and handled below.
	if !strings.Contains(repo, "://") {
		// Expect "[user@]host:path". The host is everything before the first
		// colon, minus any userinfo.
		colon := strings.Index(repo, ":")
		if colon <= 0 {
			return "", fmt.Errorf("repo URL %q is not a recognized git URL", repo)
		}
		host := repo[:colon]
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		// A scp-style path portion must be non-empty; a bare "host:" is malformed.
		if repo[colon+1:] == "" {
			return "", fmt.Errorf("repo URL %q is not a recognized git URL", repo)
		}
		// host is everything before the FIRST colon, so it structurally cannot
		// itself contain a ':' — no port to strip (scp-style hosts carry none).
		if host == "" {
			return "", fmt.Errorf("repo URL %q has no host", repo)
		}
		return strings.ToLower(host), nil
	}

	u, err := url.Parse(repo)
	if err != nil {
		return "", fmt.Errorf("repo URL %q is not parseable: %w", repo, err)
	}
	host := u.Hostname() // drops userinfo and port
	if host == "" {
		return "", fmt.Errorf("repo URL %q has no host", repo)
	}
	return strings.ToLower(host), nil
}

// RepoHostAllowed reports whether repo's host is on the allowlist, using a
// case-insensitive exact host match (no suffix/subdomain matching — an entry
// "github.com" does NOT authorize "evil.github.com.attacker.net"). A malformed
// or hostless repo returns false so the credential is never forwarded to a URL
// we could not confidently classify (fail-closed; see RepoHost).
func RepoHostAllowed(repo string, hosts []string) bool {
	host, err := RepoHost(repo)
	if err != nil {
		return false
	}
	for _, h := range hosts {
		if strings.EqualFold(host, strings.TrimSpace(h)) {
			return true
		}
	}
	return false
}
