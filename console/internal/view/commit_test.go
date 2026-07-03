package view

import "testing"

// CommitURL derives a web link for a commit from the clone URL, best-effort:
// https/http and scp-style git@ remotes map onto <https base>/commit/<sha>;
// anything else yields "" (the template renders a plain, unlinked SHA).
func TestCommitURL(t *testing.T) {
	sha := "cafebabe1234567890"
	tests := []struct {
		name string
		repo string
		want string
	}{
		{"https with .git", "https://github.com/acme/site.git", "https://github.com/acme/site/commit/" + sha},
		{"https without .git", "https://github.com/acme/site", "https://github.com/acme/site/commit/" + sha},
		{"https trailing slash", "https://github.com/acme/site/", "https://github.com/acme/site/commit/" + sha},
		{"http", "http://git.example.com/acme/site.git", "http://git.example.com/acme/site/commit/" + sha},
		{"gitlab subgroup", "https://gitlab.com/grp/sub/site.git", "https://gitlab.com/grp/sub/site/commit/" + sha},
		{"scp-style git@", "git@github.com:acme/site.git", "https://github.com/acme/site/commit/" + sha},
		{"scp-style no .git", "git@github.com:acme/site", "https://github.com/acme/site/commit/" + sha},
		{"ssh scheme not web-derivable", "ssh://git@github.com/acme/site.git", ""},
		{"garbage", "not a url", ""},
		{"empty repo", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CommitURL(tt.repo, sha); got != tt.want {
				t.Fatalf("CommitURL(%q) = %q, want %q", tt.repo, got, tt.want)
			}
		})
	}
	t.Run("empty sha yields no link", func(t *testing.T) {
		if got := CommitURL("https://github.com/acme/site.git", ""); got != "" {
			t.Fatalf("CommitURL with empty sha = %q, want empty", got)
		}
	})
}

// ShortSHA abbreviates a commit SHA to 7 characters for display.
func TestShortSHA(t *testing.T) {
	if got := ShortSHA("cafebabe1234567890"); got != "cafebab" {
		t.Fatalf("ShortSHA = %q, want cafebab", got)
	}
	if got := ShortSHA("abc"); got != "abc" {
		t.Fatalf("ShortSHA short input = %q, want abc", got)
	}
	if got := ShortSHA(""); got != "" {
		t.Fatalf("ShortSHA empty = %q, want empty", got)
	}
}
