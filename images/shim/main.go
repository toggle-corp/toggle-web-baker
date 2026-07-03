// Command shim is the phase wrapper that measures a build phase's TRUE peak
// memory. The operator injects this static binary into every user phase
// container (setup/fetch/build) and rewrites the phase command to
//
//	/baker/shim -- <user command...>
//
// The shim execs the user command verbatim (same argv/env/cwd/uid), waits,
// reads the kernel's per-cgroup high-water mark, appends
// `peakMemoryBytes=<n>` to the container termination log, and exits with the
// child's code. Sampling (metrics-server, kubelet stats) can never observe a
// true maximum; the cgroup peak is exact — it is the max of memory.current,
// the very value the OOM killer compares against the limit.
//
// Modes:
//
//	shim install <dest>   copy the running binary to <dest> (0555) — used by
//	                      the shim-install init container to place the binary
//	                      on the shared emptyDir (scratch has no cp).
//	shim -- <cmd...>      wrap mode, described above.
//
// Env overrides (tests): SHIM_CGROUP_ROOT (default /sys/fs/cgroup),
// TERMINATION_LOG (default /dev/termination-log — same convention as the du
// image).
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	args := os.Args[1:]
	switch {
	case len(args) == 2 && args[0] == "install":
		if err := install(args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "shim install: %v\n", err)
			os.Exit(1)
		}
	case len(args) >= 2 && args[0] == "--":
		os.Exit(wrap(args[1:]))
	default:
		fmt.Fprintf(os.Stderr, "usage: shim install <dest> | shim -- <cmd...>\n")
		os.Exit(2)
	}
}

// install copies the running binary to dest, world-executable so any phase
// UID can run it.
func install(dest string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	src, err := os.Open(self)
	if err != nil {
		return fmt.Errorf("open self: %w", err)
	}
	defer func() { _ = src.Close() }()
	tmp := dest + ".tmp"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o555)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return fmt.Errorf("copy: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	// Rename for atomicity: a phase container racing the install (impossible
	// today — init containers are ordered — but cheap to be correct about)
	// never sees a half-written binary.
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// wrap runs the user command as a child, records the cgroup memory peak to the
// termination log, and returns the child's exit code. The measurement is
// best-effort by design: any failure to read the peak or write the log must
// NEVER change the phase's outcome, so errors go to stderr only.
func wrap(argv []string) int {
	// Echo the command to STDOUT so it reads as part of the build output, like
	// `set -x`. Deliberately no `shim:` prefix (that prefix is reserved for shim
	// diagnostics on stderr); this belongs to the phase's own log stream.
	fmt.Printf("+ %s\n", quoteArgv(argv))

	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv is the operator-supplied phase command
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	// Forward every forwardable signal (SIGTERM on job abort, etc.) so the
	// child observes the same lifecycle it would as PID 1.
	sigc := make(chan os.Signal, 16)
	signal.Notify(sigc)
	defer signal.Stop(sigc)

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "shim: start %q: %v\n", argv[0], err)
		reportPeak()
		return 127
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
loop:
	for {
		select {
		case sig := <-sigc:
			if s, ok := sig.(syscall.Signal); ok && s != syscall.SIGCHLD {
				_ = cmd.Process.Signal(sig)
			}
		case waitErr = <-done:
			break loop
		}
	}

	reportPeak()
	return exitCode(cmd, waitErr)
}

// quoteArgv renders argv as a single shell-safe command line, space-joined,
// so it can be printed verbatim and pasted back into a POSIX shell.
func quoteArgv(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = quoteArg(a)
	}
	return strings.Join(quoted, " ")
}

// quoteArg applies POSIX single-quote quoting to one argument. Words made only
// of shell-safe characters stay bare; anything else is wrapped in single quotes
// with embedded single quotes escaped via the standard '\'' idiom (close quote,
// escaped literal quote, reopen quote), which keeps $VAR, spaces, and quotes
// literal. The empty string becomes '' so it survives as a distinct argument.
func quoteArg(s string) string {
	if s != "" && !strings.ContainsFunc(s, func(r rune) bool { return !isShellSafe(r) }) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// isShellSafe reports whether r may appear unquoted in a shell word, matching
// ^[A-Za-z0-9_@%+=:,./-]$. Note: non-ASCII runes are treated as unsafe so we
// never rely on locale-dependent shell word-splitting; they are quoted, which
// is always safe.
func isShellSafe(r rune) bool {
	switch {
	case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("_@%+=:,./-", r)
}

// exitCode maps the child's wait result to the code this process should exit
// with: the child's own code, 128+signal when signal-killed (shell
// convention, so an OOM SIGKILL still reads as 137), or 127 when wait failed
// for a non-exit reason.
func exitCode(cmd *exec.Cmd, waitErr error) int {
	if waitErr == nil {
		return 0
	}
	if cmd.ProcessState != nil {
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		if code := cmd.ProcessState.ExitCode(); code >= 0 {
			return code
		}
	}
	fmt.Fprintf(os.Stderr, "shim: wait: %v\n", waitErr)
	return 127
}

// reportPeak reads the container cgroup's memory high-water mark and appends
// `peakMemoryBytes=<n>` to the termination log. With cgroup namespaces
// (default on cgroup v2 pods) the container sees its OWN cgroup at
// /sys/fs/cgroup, so memory.peak is exactly this phase's peak. Falls back to
// the cgroup v1 file. Silent no-op (stderr note) when neither is readable.
func reportPeak() {
	root := os.Getenv("SHIM_CGROUP_ROOT")
	if root == "" {
		root = "/sys/fs/cgroup"
	}
	var peak string
	for _, f := range []string{root + "/memory.peak", root + "/memory/memory.max_usage_in_bytes"} {
		raw, err := os.ReadFile(f) //nolint:gosec // fixed cgroup paths under an env-selected root
		if err != nil {
			continue
		}
		peak = strings.TrimSpace(string(raw))
		break
	}
	if peak == "" {
		fmt.Fprintln(os.Stderr, "shim: no readable cgroup memory peak (cgroup v2 with cgroupns required); skipping report")
		return
	}
	log := os.Getenv("TERMINATION_LOG")
	if log == "" {
		log = "/dev/termination-log"
	}
	f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // kubelet-provided termination log path
	if err != nil {
		fmt.Fprintf(os.Stderr, "shim: open termination log: %v\n", err)
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprintf(f, "peakMemoryBytes=%s\n", peak); err != nil {
		fmt.Fprintf(os.Stderr, "shim: write termination log: %v\n", err)
	}
}
