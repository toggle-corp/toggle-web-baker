# shim

Phase wrapper that measures a build phase's **true peak memory** — the
kernel-tracked cgroup high-water mark, not a sampled approximation.

The operator injects this static binary into every user phase container
(setup/fetch/build) via a shared emptyDir and rewrites the phase command:

    /baker/shim -- <user command...>

The shim execs the user command verbatim (same argv, env, cwd, uid; signals
forwarded), waits, reads `/sys/fs/cgroup/memory.peak` (cgroup v2; falls back
to v1 `memory.max_usage_in_bytes`), appends `peakMemoryBytes=<n>` to the
container termination log, and exits with the child's code (128+signal when
signal-killed). The operator parses the termination message into
`status.build.steps[].peakMemoryBytes`.

Properties:

- **Exact**: `memory.peak` is the max of `memory.current` — the value the OOM
  killer compares against the limit, so it is directly actionable for tuning
  `spec.pipeline.phases.<p>.memoryLimit`. Includes page cache, like the OOM
  decision itself.
- **Per-phase**: every phase is its own container = its own cgroup.
- **Best-effort by design**: a failure to read the peak or write the log never
  changes the phase's outcome.
- **OOM-safe**: `OOMKilled` detection is unchanged — runtimes flag it from the
  cgroup's oom event regardless of which process died (and on cgroup v2 the
  kubelet sets `memory.oom.group=1`, killing shim and child together).

Modes:

- `shim install <dest>` — copy the running binary to `<dest>` (0555, atomic
  rename). Used by the `shim-install` init container; scratch has no `cp`.
- `shim -- <cmd...>` — wrap mode, described above.

Env overrides (used by `images/test/shim_test.sh`): `SHIM_CGROUP_ROOT`
(default `/sys/fs/cgroup`), `TERMINATION_LOG` (default
`/dev/termination-log`).
