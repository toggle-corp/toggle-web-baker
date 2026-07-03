package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuoteArg(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare word", "yarn", "yarn"},
		{"bare with allowed punctuation", "a-b_c.d/e:f,g@h%i+j=k", "a-b_c.d/e:f,g@h%i+j=k"},
		{"space", "a b", "'a b'"},
		{"single quote", "a'b", `'a'\''b'`},
		// Each embedded ' becomes the 4-char sequence '\'' (close, escaped
		// literal quote, reopen); two of them wrapped in outer quotes.

		{"double quote", `a"b`, `'a"b'`},
		{"empty string", "", "''"},
		{"dollar var", "$VAR", "'$VAR'"},
		// Non-ASCII runes are outside ^[A-Za-z0-9_@%+=:,./-]+$, so they are
		// quoted rather than left bare — safe under any locale.
		{"unicode", "café", "'café'"},
		{"unicode with space", "café au lait", "'café au lait'"},
		{"only single quotes", "''", `''\'''\'''`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quoteArg(tc.in); got != tc.want {
				t.Errorf("quoteArg(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestQuoteArgv(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"simple", []string{"yarn", "install", "--frozen-lockfile"}, "yarn install --frozen-lockfile"},
		{"sh -c with quoting", []string{"sh", "-c", `echo "a b"`}, `sh -c 'echo "a b"'`},
		{"empty argv", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quoteArgv(tc.in); got != tc.want {
				t.Errorf("quoteArgv(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestWrapEchoesCommand builds the shim and runs it against /bin/echo, asserting
// the first stdout line is the `+ `-prefixed shell-quoted argv, followed by the
// echo output. Hermetic: skips if /bin/echo is absent.
func TestWrapEchoesCommand(t *testing.T) {
	if _, err := os.Stat("/bin/echo"); err != nil {
		t.Skip("/bin/echo not present; skipping integration test")
	}

	bin := filepath.Join(t.TempDir(), "shim")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build shim: %v\n%s", err, out)
	}

	// Point cgroup/termination-log env at nonexistent paths so reportPeak is a
	// harmless no-op (its diagnostics go to stderr, not stdout).
	cmd := exec.Command(bin, "--", "/bin/echo", "hello", "world")
	cmd.Env = append(os.Environ(),
		"SHIM_CGROUP_ROOT="+filepath.Join(t.TempDir(), "nope"),
		"TERMINATION_LOG="+filepath.Join(t.TempDir(), "termlog"),
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run shim: %v", err)
	}
	got := string(out)
	want := "+ /bin/echo hello world\nhello world\n"
	if got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, "+ /bin/echo hello world\n") {
		t.Errorf("stdout does not start with echoed command line: %q", got)
	}
}
