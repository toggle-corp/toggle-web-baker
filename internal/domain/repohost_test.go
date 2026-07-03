package domain

import "testing"

func TestRepoHostAllowed(t *testing.T) {
	tests := []struct {
		name  string
		repo  string
		hosts []string
		want  bool
	}{
		{"https match", "https://github.com/org/repo.git", []string{"github.com"}, true},
		{"scp match", "git@github.com:org/repo.git", []string{"github.com"}, true},
		{"ssh match", "ssh://git@github.com/org/repo", []string{"github.com"}, true},
		{"case-insensitive repo", "https://GitHub.com/org/repo", []string{"github.com"}, true},
		{"case-insensitive allowlist entry", "https://github.com/org/repo", []string{"GITHUB.COM"}, true},
		{"multiple hosts, second matches", "https://gitlab.com/org/repo", []string{"github.com", "gitlab.com"}, true},
		{"host not allowlisted", "https://evil.com/x.git", []string{"github.com"}, false},
		{"no suffix match: subdomain attack", "https://evil.github.com.attacker.net/x.git", []string{"github.com"}, false},
		{"empty allowlist never matches", "https://github.com/org/repo", nil, false},
		{"malformed repo is false", "not a url", []string{"github.com"}, false},
		{"empty repo is false", "", []string{"github.com"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RepoHostAllowed(tt.repo, tt.hosts); got != tt.want {
				t.Fatalf("RepoHostAllowed(%q, %v) = %v, want %v", tt.repo, tt.hosts, got, tt.want)
			}
		})
	}
}

func TestRepoHost(t *testing.T) {
	tests := []struct {
		name    string
		repo    string
		want    string
		wantErr bool
	}{
		{"https", "https://github.com/org/repo.git", "github.com", false},
		{"https no .git", "https://github.com/org/repo", "github.com", false},
		{"http", "http://github.com/org/repo.git", "github.com", false},
		{"https with port", "https://github.com:8443/org/repo.git", "github.com", false},
		{"https with userinfo", "https://user:tok@github.com/org/repo.git", "github.com", false},
		{"uppercase host lowercased", "https://GitHub.COM/org/repo.git", "github.com", false},
		{"scp style", "git@github.com:org/repo.git", "github.com", false},
		{"scp style no user", "github.com:org/repo.git", "github.com", false},
		{"ssh scheme", "ssh://git@github.com/org/repo", "github.com", false},
		{"ssh scheme with port", "ssh://git@github.com:22/org/repo", "github.com", false},
		{"empty rejected", "", "", true},
		{"whitespace rejected", "   ", "", true},
		{"no host rejected", "https:///org/repo", "", true},
		{"bare host no path rejected", "github.com:", "", true},
		{"garbage rejected", "not a url", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RepoHost(tt.repo)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("RepoHost(%q) = %q, want error", tt.repo, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("RepoHost(%q) unexpected error: %v", tt.repo, err)
			}
			if got != tt.want {
				t.Fatalf("RepoHost(%q) = %q, want %q", tt.repo, got, tt.want)
			}
		})
	}
}
