package view

import "strings"

// CommitURL derives the web page for a commit from the app's clone URL,
// best-effort: http(s) remotes and scp-style git@host:path remotes map onto
// "<https base>/commit/<sha>" (GitHub's shape; GitLab and Gitea redirect it).
// Every other shape — ssh://, garbage, empty — returns "" and the template
// renders a plain, unlinked SHA. Never guesses: a wrong link is worse than none.
func CommitURL(repo, sha string) string {
	if sha == "" {
		return ""
	}
	base := ""
	switch {
	case strings.HasPrefix(repo, "https://"), strings.HasPrefix(repo, "http://"):
		base = repo
	case strings.HasPrefix(repo, "git@"):
		// git@host:path → https://host/path
		hostPath := strings.TrimPrefix(repo, "git@")
		host, path, ok := strings.Cut(hostPath, ":")
		if !ok || host == "" || path == "" {
			return ""
		}
		base = "https://" + host + "/" + path
	default:
		return ""
	}
	base = strings.TrimSuffix(strings.TrimSuffix(base, "/"), ".git")
	if strings.ContainsAny(base, " \t") {
		return ""
	}
	return base + "/commit/" + sha
}

// CommitLink derives the commit web URL against this app's repo — the
// template-facing wrapper over CommitURL.
func (a App) CommitLink(sha string) string { return CommitURL(a.Repo, sha) }

// ShortSHA abbreviates a commit SHA to the conventional 7 characters.
func ShortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
