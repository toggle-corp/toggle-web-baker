# App CRD reference

<!-- GENERATED from config/crd/baker.toggle-corp.com_apps.yaml by
     hack/gen-crd-reference.py — DO NOT EDIT. Run `just manifests`
     (or `just crd-docs`) after changing api/v1alpha1/*_types.go. -->

`apps.baker.toggle-corp.com` — kind `App`, group `baker.toggle-corp.com`, version `v1alpha1`, shortNames: `bakerapp`.

This is generated from the CRD schema (the +kubebuilder godoc on the Go
types), so it matches `kubectl explain app.<field>` exactly and cannot
drift. Defaults shown as `default` are CRD-level; a field documented as
falling back to an operator/chart config has no CRD default (the operator
resolves it at runtime).

## `.spec`

AppSpec is the desired state: operational tunables for one app.

- **`spec.auth`** — `object`, optional
  - _CEL_: `(has(self.passwordHash) ? 1 : 0) + (has(self.secretRef) ? 1 : 0) == 1` — auth requires exactly one of passwordHash or secretRef
  AuthConfig configures optional HTTP basic auth. Exactly one of passwordHash or secretRef must be set.
  - **`spec.auth.passwordHash`** — `string`, optional
  - **`spec.auth.secretRef`** — `object`, optional
    AuthSecretRef points at a Secret key holding a bcrypt/htpasswd line.
    - **`spec.auth.secretRef.key`** — `string`, **required**
    - **`spec.auth.secretRef.name`** — `string`, **required**
- **`spec.group`** — `string`, optional, maxLen `63`, pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
  Group is an optional, purely-informational project label used by the console to group and filter apps (e.g. all instances of one site).
- **`spec.history`** — `object`, optional
  History tunes how many terminal builds the operator retains in status (recent ring + failed-only list). Absent means the operator-config defaults apply to both lists.
  - **`spec.history.keepFailed`** — `integer`, optional, min `1`, max `50`
    KeepFailed is how many FAILED builds status.failedBuildHistory retains, independent of the recent ring so a burst of successes can't evict a failure needed for debugging. 0 means the operator default (config historyKeepFailed, default 10; chart-owned so a chart may override it).
  - **`spec.history.keepRecent`** — `integer`, optional, min `1`, max `50`
    KeepRecent is how many recent terminal builds (any result) the newest-first status.buildHistory ring retains. 0 means the operator default (config historyKeepRecent, default 5; chart-owned so a chart may override it).
- **`spec.ingress`** — `object`, **required**
  IngressConfig describes the public ingress for the served bundle.
  - **`spec.ingress.annotations`** — `map<string,string>`, optional
    - _CEL_: `!('traefik.ingress.kubernetes.io/router.middlewares' in self)` — ingress.annotations may not set the reserved key traefik.ingress.kubernetes.io/router.middlewares (managed by the operator for basic-auth)
    Annotations are user-supplied ingress annotations (cert-manager, custom Traefik routers, rate-limit, etc.) merged onto the generated Ingress. They are applied FIRST; the operator overlays its own managed keys LAST so the operator always wins on a conflict (see children.go ingress()). The basic-auth router.middlewares key is RESERVED — the CEL rule below rejects it in the map so a user can't strip or redirect the operator's basic-auth middleware (which would expose the served bundle). Free-form otherwise; an allowlist is deferred as future hardening if tenancy concerns grow.
  - **`spec.ingress.className`** — `string`, optional
  - **`spec.ingress.host`** — `string`, **required**
  - **`spec.ingress.tls`** — `object`, optional
    TLSConfig configures TLS termination at the Ingress.
    - **`spec.ingress.tls.secretName`** — `string`, **required**
- **`spec.keepReleases`** — `integer`, optional, default `0`, min `0`
  KeepReleases is the total number of releases retained on the output volume, newest first; the copier prunes after each publish and the manual release-prune uses the same budget. The current and previous release are ALWAYS protected (and count toward the budget), so 0 — the default — means "keep only the protected releases".
- **`spec.pipeline`** — `object`, **required**
  - _CEL_: `has(self.nodeVersion) || has(self.phases.build.image)` — build needs an image: set nodeVersion or build.image under pipeline
  - _CEL_: `!has(self.timeout) || duration(self.timeout) > duration('0s')` — pipeline.timeout must be a positive duration; omit it to use the operator default
  Pipeline is HOW the app is built: toolchain, timeout, and the ordered setup/fetch/build phases. Required — every app must build something.
  - **`spec.pipeline.nodeVersion`** — `integer`, optional, min `1`
    NodeVersion selects an operator-managed node toolchain by MAJOR version (e.g. 18). The operator resolves it to a digest-pinned image + numeric UID + writable HOME, so the app need not set image/runAsUser. A phase may still override with its own image (fully BYO for that phase). Available majors are operator/chart config; an unknown version fails the app at reconcile. Omit to supply build.image yourself.
  - **`spec.pipeline.packageManager`** — `string`, **required**, enum `yarn`, `pnpm`
    PackageManager selects the JS package manager (yarn|pnpm). Required with NO default: the choice drives the cache volume layout AND the operator's default setup-install command (when setup is omitted), so an app must state it explicitly rather than inherit a silent default.
  - **`spec.pipeline.phases`** — `object`, **required**
    PhasesSpec is the ordered build pipeline: setup → fetch → build. setup and fetch are optional (an app may install/fetch nothing); build is required (it produces the served bundle). The operator runs them in this fixed order regardless of map/field order.
    - **`spec.pipeline.phases.build`** — `object`, **required**
      - _CEL_: `has(self.command) && size(self.command) > 0` — pipeline.phases.build.command is required
      - _CEL_: `!has(self.outputDir) || self.outputDir.split('/').all(s, size(s) > 0 && s != '.' && s != '..')` — build.outputDir must be a relative path with no empty, '.' or '..' segments
      - _CEL_: `!has(self.env) || !has(self.envMap) || self.env.all(e, !(e.name in self.envMap))` — a key cannot appear in both env and envMap
      BuildPhaseSpec is the build phase: a PhaseSpec plus the output directory the copier publishes. build carries more than setup/fetch, so it has its own type (setup/fetch stay plain PhaseSpec). The empty-segment check is size(s) > 0, NOT a comparison against an empty CEL string literal: gofmt's doc-comment formatter curls two adjacent single quotes into a Unicode right quote, which silently corrupts the rule.
      - **`spec.pipeline.phases.build.command`** — `array<string>`, optional
      - **`spec.pipeline.phases.build.env`** — `array<object>`, optional
        - **`spec.pipeline.phases.build.env.name`** — `string`, **required**
        - **`spec.pipeline.phases.build.env.value`** — `string`, optional
        - **`spec.pipeline.phases.build.env.valueFrom`** — `object`, optional
          EnvVarSource is the inline (NO $ref) source for a public EnvVar. Only a ConfigMap key reference is permitted; secretKeyRef is intentionally absent so secrets can never leak into setup/build env via this type.
          - **`spec.pipeline.phases.build.env.valueFrom.configMapKeyRef`** — `object`, optional
            ConfigMapKeySelector selects a key from a ConfigMap in the app namespace.
            - **`spec.pipeline.phases.build.env.valueFrom.configMapKeyRef.key`** — `string`, **required**
            - **`spec.pipeline.phases.build.env.valueFrom.configMapKeyRef.name`** — `string`, **required**
      - **`spec.pipeline.phases.build.envMap`** — `map<string,string>`, optional
        EnvMap is a literal-only companion to env: a plain map of Name→Value pairs for the common "just set some build-time constants" case, without the per-entry {name,value} boilerplate of env. It carries LITERAL VALUES ONLY — there is no valueFrom here; when you need a configMapKeyRef (or any other source) use env instead. A key MUST NOT appear in both env and envMap: the overlap is ambiguous ("which value wins?") and is rejected at admission (see the CEL rule on PhaseSpec). At runtime the operator injects env first, then the envMap entries sorted by key, so the phase container's env ordering is deterministic.
      - **`spec.pipeline.phases.build.image`** — `string`, optional
      - **`spec.pipeline.phases.build.memoryLimit`** — `string`, optional, pattern `^[0-9]+(\.[0-9]+)?(Ki|Mi|Gi|Ti|k|M|G|T)?$`
        MemoryLimit is the per-phase container memory ceiling (a k8s quantity, e.g. "2Gi"). When omitted the operator supplies a per-phase default (operator config owns the defaults, NOT the CRD — no kubebuilder default here). The memory REQUEST is always pinned equal to the limit (incompressible ⇒ Guaranteed QoS), so a heavy build cannot OOM the node with a low request. Shared by setup/fetch/build via PhaseSpec. The pattern admits a memory quantity (plain bytes or a binary/decimal suffix) — deliberately narrower than resource.Quantity (no exponents, no milli), which is nonsensical for a memory ceiling. The operator still falls back to its default on any value that fails to parse (defense for pre-rule objects).
      - **`spec.pipeline.phases.build.outputDir`** — `string`, optional, default `dist`, maxLen `256`, pattern `^[a-zA-Z0-9_.][a-zA-Z0-9_./-]*$`
        OutputDir is the subdir of the workspace holding the built bundle (the copier's OUTPUT_DIR). The "dist" default is declared here so it shows in kubectl explain / the stored spec; the copier still treats empty as "dist" for objects admitted before the default. Must be a safe relative path. Two layers: the RE2 pattern restricts the character set (rejecting spaces/shell metachars and a leading "/"), and a CEL rule on this type rejects any empty, "." or ".." path SEGMENT (RE2 has no lookaround and can't do a per-segment check). The segment rule also blocks the "." whole-dir footgun (which would publish the entire workspace) and trailing/duplicate slashes, while still allowing dotted names like "assets..min".
      - **`spec.pipeline.phases.build.runAsUser`** — `integer`, optional, min `1`, format `int64`
        RunAsUser pins this phase container's numeric UID. The build pod sets runAsNonRoot WITHOUT a UID, so an image whose USER is a non-numeric name (e.g. cimg/node's `circleci`) is rejected at admission — the kubelet cannot verify a named user is non-root (CreateContainerConfigError). Set this to the image's numeric non-root UID to satisfy the constraint. Must be > 0 (non-root).
    - **`spec.pipeline.phases.fetch`** — `object`, optional
      - _CEL_: `!has(self.secrets) || size(self.secrets) == 0 || (has(self.command) && size(self.command) > 0)` — secrets require a fetch.command to consume them
      - _CEL_: `!has(self.env) || !has(self.envMap) || self.env.all(e, !(e.name in self.envMap))` — a key cannot appear in both env and envMap
      FetchPhaseSpec is the fetch phase: a PhaseSpec plus the Secret-sourced env it alone may consume. Secrets live here (not spec-wide) so the "secrets are fetch-only" boundary is STRUCTURAL — no other phase type can carry them.
      - **`spec.pipeline.phases.fetch.command`** — `array<string>`, optional
      - **`spec.pipeline.phases.fetch.env`** — `array<object>`, optional
        - **`spec.pipeline.phases.fetch.env.name`** — `string`, **required**
        - **`spec.pipeline.phases.fetch.env.value`** — `string`, optional
        - **`spec.pipeline.phases.fetch.env.valueFrom`** — `object`, optional
          EnvVarSource is the inline (NO $ref) source for a public EnvVar. Only a ConfigMap key reference is permitted; secretKeyRef is intentionally absent so secrets can never leak into setup/build env via this type.
          - **`spec.pipeline.phases.fetch.env.valueFrom.configMapKeyRef`** — `object`, optional
            ConfigMapKeySelector selects a key from a ConfigMap in the app namespace.
            - **`spec.pipeline.phases.fetch.env.valueFrom.configMapKeyRef.key`** — `string`, **required**
            - **`spec.pipeline.phases.fetch.env.valueFrom.configMapKeyRef.name`** — `string`, **required**
      - **`spec.pipeline.phases.fetch.envMap`** — `map<string,string>`, optional
        EnvMap is a literal-only companion to env: a plain map of Name→Value pairs for the common "just set some build-time constants" case, without the per-entry {name,value} boilerplate of env. It carries LITERAL VALUES ONLY — there is no valueFrom here; when you need a configMapKeyRef (or any other source) use env instead. A key MUST NOT appear in both env and envMap: the overlap is ambiguous ("which value wins?") and is rejected at admission (see the CEL rule on PhaseSpec). At runtime the operator injects env first, then the envMap entries sorted by key, so the phase container's env ordering is deterministic.
      - **`spec.pipeline.phases.fetch.image`** — `string`, optional
      - **`spec.pipeline.phases.fetch.memoryLimit`** — `string`, optional, pattern `^[0-9]+(\.[0-9]+)?(Ki|Mi|Gi|Ti|k|M|G|T)?$`
        MemoryLimit is the per-phase container memory ceiling (a k8s quantity, e.g. "2Gi"). When omitted the operator supplies a per-phase default (operator config owns the defaults, NOT the CRD — no kubebuilder default here). The memory REQUEST is always pinned equal to the limit (incompressible ⇒ Guaranteed QoS), so a heavy build cannot OOM the node with a low request. Shared by setup/fetch/build via PhaseSpec. The pattern admits a memory quantity (plain bytes or a binary/decimal suffix) — deliberately narrower than resource.Quantity (no exponents, no milli), which is nonsensical for a memory ceiling. The operator still falls back to its default on any value that fails to parse (defense for pre-rule objects).
      - **`spec.pipeline.phases.fetch.runAsUser`** — `integer`, optional, min `1`, format `int64`
        RunAsUser pins this phase container's numeric UID. The build pod sets runAsNonRoot WITHOUT a UID, so an image whose USER is a non-numeric name (e.g. cimg/node's `circleci`) is rejected at admission — the kubelet cannot verify a named user is non-root (CreateContainerConfigError). Set this to the image's numeric non-root UID to satisfy the constraint. Must be > 0 (non-root).
      - **`spec.pipeline.phases.fetch.secrets`** — `array<object>`, optional
        Secrets are Secret-sourced env injected into the FETCH phase ONLY.
        - **`spec.pipeline.phases.fetch.secrets.name`** — `string`, **required**
        - **`spec.pipeline.phases.fetch.secrets.valueFrom`** — `object`, **required**
          EnvVarWithSecretSource is the inline source for a secret-backed env var.
          - **`spec.pipeline.phases.fetch.secrets.valueFrom.secretKeyRef`** — `object`, **required**
            SecretKeySelector selects a key from a Secret in the app namespace.
            - **`spec.pipeline.phases.fetch.secrets.valueFrom.secretKeyRef.key`** — `string`, **required**
            - **`spec.pipeline.phases.fetch.secrets.valueFrom.secretKeyRef.name`** — `string`, **required**
    - **`spec.pipeline.phases.setup`** — `object`, optional
      - _CEL_: `!has(self.skip) || !self.skip || (!has(self.command) && !has(self.image) && !has(self.env) && !has(self.envMap) && !has(self.runAsUser) && !has(self.memoryLimit))` — setup.skip cannot be combined with other setup fields
      - _CEL_: `!has(self.env) || !has(self.envMap) || self.env.all(e, !(e.name in self.envMap))` — a key cannot appear in both env and envMap
      Setup is the dependency-install phase. Omitted (with pipeline.nodeVersion set) means the operator injects a default install command for the selected packageManager; setup.skip:true means no setup phase at all. See SetupPhaseSpec.
      - **`spec.pipeline.phases.setup.command`** — `array<string>`, optional
      - **`spec.pipeline.phases.setup.env`** — `array<object>`, optional
        - **`spec.pipeline.phases.setup.env.name`** — `string`, **required**
        - **`spec.pipeline.phases.setup.env.value`** — `string`, optional
        - **`spec.pipeline.phases.setup.env.valueFrom`** — `object`, optional
          EnvVarSource is the inline (NO $ref) source for a public EnvVar. Only a ConfigMap key reference is permitted; secretKeyRef is intentionally absent so secrets can never leak into setup/build env via this type.
          - **`spec.pipeline.phases.setup.env.valueFrom.configMapKeyRef`** — `object`, optional
            ConfigMapKeySelector selects a key from a ConfigMap in the app namespace.
            - **`spec.pipeline.phases.setup.env.valueFrom.configMapKeyRef.key`** — `string`, **required**
            - **`spec.pipeline.phases.setup.env.valueFrom.configMapKeyRef.name`** — `string`, **required**
      - **`spec.pipeline.phases.setup.envMap`** — `map<string,string>`, optional
        EnvMap is a literal-only companion to env: a plain map of Name→Value pairs for the common "just set some build-time constants" case, without the per-entry {name,value} boilerplate of env. It carries LITERAL VALUES ONLY — there is no valueFrom here; when you need a configMapKeyRef (or any other source) use env instead. A key MUST NOT appear in both env and envMap: the overlap is ambiguous ("which value wins?") and is rejected at admission (see the CEL rule on PhaseSpec). At runtime the operator injects env first, then the envMap entries sorted by key, so the phase container's env ordering is deterministic.
      - **`spec.pipeline.phases.setup.image`** — `string`, optional
      - **`spec.pipeline.phases.setup.memoryLimit`** — `string`, optional, pattern `^[0-9]+(\.[0-9]+)?(Ki|Mi|Gi|Ti|k|M|G|T)?$`
        MemoryLimit is the per-phase container memory ceiling (a k8s quantity, e.g. "2Gi"). When omitted the operator supplies a per-phase default (operator config owns the defaults, NOT the CRD — no kubebuilder default here). The memory REQUEST is always pinned equal to the limit (incompressible ⇒ Guaranteed QoS), so a heavy build cannot OOM the node with a low request. Shared by setup/fetch/build via PhaseSpec. The pattern admits a memory quantity (plain bytes or a binary/decimal suffix) — deliberately narrower than resource.Quantity (no exponents, no milli), which is nonsensical for a memory ceiling. The operator still falls back to its default on any value that fails to parse (defense for pre-rule objects).
      - **`spec.pipeline.phases.setup.runAsUser`** — `integer`, optional, min `1`, format `int64`
        RunAsUser pins this phase container's numeric UID. The build pod sets runAsNonRoot WITHOUT a UID, so an image whose USER is a non-numeric name (e.g. cimg/node's `circleci`) is rejected at admission — the kubelet cannot verify a named user is non-root (CreateContainerConfigError). Set this to the image's numeric non-root UID to satisfy the constraint. Must be > 0 (non-root).
      - **`spec.pipeline.phases.setup.skip`** — `boolean`, optional
        Skip opts out of the setup phase entirely: no user command AND no operator-injected default install. Mutually exclusive with every other setup field (see the CEL rule on this type).
  - **`spec.pipeline.submodules`** — `boolean`, optional
  - **`spec.pipeline.timeout`** — `string`, optional
    Timeout bounds the WHOLE build pipeline (all phases) as a Go duration string (e.g. "1h", "90m", "1h30m"; max unit is hours — no days). When unset the operator supplies the default from its config (NO kubebuilder default here — operator config owns it). A pointer so unset is truly absent: a value type would always serialize as "0s", defeating the has() guard in the positive-duration CEL rule on PipelineSpec.
- **`spec.ref`** — `string`, optional, default `HEAD`
- **`spec.repo`** — `string`, **required**, minLen `1`, pattern `^(https?://|git@|ssh://)[^\s]+$`
  Repo is the clone URL, handed verbatim to `git clone`. The pattern is a loose shape check (https / ssh / scp-style) that catches garbage at admission instead of minutes later at clone time — it must stay wide enough to never reject a URL git can clone.
- **`spec.repoAuth`** — `object`, optional
  RepoAuth is the optional per-App git credential for spec.repo. When set it FULLY replaces the operator-global credential (clone AND commit-watch) and the operator mounts the user's Secret directly with NO host allowlist check (the user's own credential for their own repo — design Q4/Q6). Absent means the operator-global credential applies when the repo host is allowlisted, else anonymous git.
  - **`spec.repoAuth.secretRef`** — `object`, **required**
    RepoAuthSecretRef names the Secret holding the git credential. It has NO key selectors: the keys are the well-known basic-auth pair (username/password), matching kubernetes.io/basic-auth and the GIT_CREDENTIAL_DIR mount convention the clone/watch images consume. Fixing the keys keeps the CRD, the mount, and the operator-global source Secret on one shape — no per-app key wiring to drift.
    - **`spec.repoAuth.secretRef.name`** — `string`, **required**
      Name is the Secret in the App's OWN namespace. The operator mounts it directly (no copy) into the clone initContainer and the watch CronJob; the host allowlist is NOT applied (it is the user's own credential for their own repo — see design Q4). The reconciler validates it exists with non-empty username/password and surfaces a Degraded condition otherwise (naming the Secret only, never its values).
- **`spec.scheduledBuilds`** — `object`, optional
  ScheduledBuilds enables time-based rebuilds (the clock CronJob). Absent means DISABLED — apps that need periodic data-refresh builds must opt in explicitly. Deliberately a struct with a required Enabled rather than a defaulted flat field, so `scheduledBuilds: {schedule: ...}` without an explicit enabled is rejected at admission instead of silently doing nothing.
  - **`spec.scheduledBuilds.alertThreshold`** — `integer`, optional, min `1`
    AlertThreshold is how many CONSECUTIVE scheduled-build failures must occur before the AppScheduledBuildsFailingThreshold alert fires. Scoped to scheduled builds only (manual/commit/spec-change failures still alert immediately via AppBuildFailed) — hourly data-refresh builds tolerate a few transient failures in a row before a human needs paging. 0 means the operator default (config scheduledAlertThreshold, default 3; chart-owned so a chart may override it).
  - **`spec.scheduledBuilds.enabled`** — `boolean`, **required**
    Enabled must be stated explicitly (no default): false keeps the config around while pausing the clock CronJob.
  - **`spec.scheduledBuilds.schedule`** — `string`, optional
    Schedule is the clock CronJob's cron expression. Empty means the operator default (config defaultSchedule, chart-owned; the CRD cannot know it).
- **`spec.storage`** — `object`, optional
  - _CEL_: `!has(self.cache) || !has(self.cache.cleanupBytes) || !has(self.cache.alertBytes) || self.cache.cleanupBytes < self.cache.alertBytes` — cache.cleanupBytes must be < cache.alertBytes
  - _CEL_: `!has(self.dataCache) || !has(self.dataCache.cleanupBytes) || !has(self.dataCache.alertBytes) || self.dataCache.cleanupBytes < self.dataCache.alertBytes` — dataCache.cleanupBytes must be < dataCache.alertBytes
  - _CEL_: `!has(self.output) || !has(self.output.alertBytes) || !has(self.output.capBytes) || self.output.alertBytes < self.output.capBytes` — output.alertBytes must be < output.capBytes
  StorageConfig groups the per-volume absolute-byte thresholds. The operator also calls domain.ValidateStorage at reconcile time (cleanup < alert < cap).
  - **`spec.storage.cache`** — `object`, optional
    CacheThresholds are absolute-byte thresholds for the regenerable cache volume. All byte fields are non-negative; 0 means unset/disabled.
    - **`spec.storage.cache.alertBytes`** — `integer`, optional, min `0`, format `int64`
    - **`spec.storage.cache.cleanupBytes`** — `integer`, optional, min `0`, format `int64`
  - **`spec.storage.dataCache`** — `object`, optional
    DataCacheThresholds adds a per-run delta budget on top of cache thresholds. All byte fields are non-negative; 0 means unset/disabled.
    - **`spec.storage.dataCache.alertBytes`** — `integer`, optional, min `0`, format `int64`
    - **`spec.storage.dataCache.cleanupBytes`** — `integer`, optional, min `0`, format `int64`
    - **`spec.storage.dataCache.runDeltaBytes`** — `integer`, optional, min `0`, format `int64`
  - **`spec.storage.output`** — `object`, optional
    OutputThresholds bound the served-bundle volume. All byte fields are non-negative; 0 means unset/disabled.
    - **`spec.storage.output.alertBytes`** — `integer`, optional, min `0`, format `int64`
    - **`spec.storage.output.capBytes`** — `integer`, optional, min `0`, format `int64`
- **`spec.watchCommits`** — `object`, optional
  WatchCommits enables commit-triggered rebuilds: a per-app watcher CronJob polls `git ls-remote` on Repo/Ref and requests a rebuild when the SHA changes. Absent means DISABLED. Coexists with ScheduledBuilds (data-driven apps still need time-based rebuilds; pure-source apps can go watch-only).
  - **`spec.watchCommits.enabled`** — `boolean`, **required**
    Enabled must be stated explicitly (no default): false keeps the config around while pausing the watcher CronJob.
  - **`spec.watchCommits.interval`** — `string`, optional, pattern `^[0-9]+[mh]$`
    Interval is how often the watcher polls the remote, as a single-unit Go duration in whole minutes (1m–59m) or whole hours (1h–23h) — CronJob schedules cannot express anything else. The pattern is a shape check (single [0-9]+m or [0-9]+h term); range checks live in domain.WatchCron and surface as a Degraded condition. Empty means the operator default (config defaultWatchInterval).

## `.status`

AppStatus is the operator-owned observed state.

- **`status.build`** — `object`, optional
  BuildStatus is the unified per-build record. It mirrors the current/last build Job in status.build, and the SAME shape is reused for every entry of status.buildHistory — one type, one renderer.
  - **`status.build.attempts`** — `integer`, optional
  - **`status.build.commit`** — `string`, optional
    Commit is the SHA that triggered a commit-watch build (empty for other triggers), captured from RebuildCommitAnnotation at Job creation.
  - **`status.build.completionTime`** — `string`, optional, format `date-time`
  - **`status.build.failedStep`** — `string`, optional
    FailedStep names the step whose failure ended the build, when Result is Failed or Aborted.
  - **`status.build.jobName`** — `string`, optional
  - **`status.build.logsRef`** — `string`, optional
  - **`status.build.message`** — `string`, optional
  - **`status.build.phase`** — `string`, optional
    BuildPhase is the lifecycle of the current/last build pod.
  - **`status.build.podName`** — `string`, optional
    PodName is the build pod for this Job, persisted so the read-only console (which can get but not list pods) can fetch logs, and so a Loki query can be scoped by pod label.
  - **`status.build.resolvedImages`** — `map<string,string>`, optional
    ResolvedImages maps each pipeline container (shim-install/clone/setup/ fetch/build/copier) to the exact image reference the build Job was created with — digest-pinned for operator-managed toolchains. Captured at Job CREATION (like SpecHashAnnotation) so it records the build that actually ran, not a later operator-config change. Bounded by the fixed pipeline shape: 6 containers today (shim-install/clone/setup/fetch/build/ copier), 8 leaves headroom for new pipeline steps without an API bump.
  - **`status.build.result`** — `string`, optional
    BuildResult is the terminal outcome of a build pod.
  - **`status.build.startTime`** — `string`, optional, format `date-time`
  - **`status.build.steps`** — `array<object>`, optional
    Steps is the ordered per-step timeline (only applicable steps).
    - **`status.build.steps.finishedAt`** — `string`, optional, format `date-time`
      FinishedAt is when the step's container terminated. Absent while the step is still running (the console derives a live duration from StartedAt).
    - **`status.build.steps.memoryLimit`** — `string`, optional
      MemoryLimit is the memory ceiling the step's container ran with, as its Kubernetes quantity string (e.g. "2Gi"), read from the build pod SPEC so it reflects the build that actually ran. Only stamped once the container has started (Running or Terminated). Rendered next to PeakMemoryBytes so peak-vs-allocated is visible per step. Empty for the synthetic release step, a step that has not started yet, or when the pod was already reaped.
    - **`status.build.steps.message`** — `string`, optional
    - **`status.build.steps.name`** — `string`, **required**
    - **`status.build.steps.peakMemoryBytes`** — `integer`, optional, format `int64`
      PeakMemoryBytes is the phase's TRUE peak memory: the kernel-tracked cgroup high-water mark (memory.peak — the max of memory.current, the value the OOM killer compares against the limit), recorded by the shim wrapper via the container termination message when the step ends. It is the number to tune spec.pipeline.phases.<p>.memoryLimit against. 0/absent when unmeasured (shim-less steps like clone/copier, pod reaped before terminal observe, or no cgroup v2 peak available).
    - **`status.build.steps.startedAt`** — `string`, optional, format `date-time`
      StartedAt is when the step's container started, read from the kubelet's container state (Running.StartedAt or Terminated.StartedAt). Absent for a step not yet reached, the synthetic release step, or a pod reaped before terminal observe.
    - **`status.build.steps.status`** — `string`, **required**
      StepStatus is the state of one ordered step in a build's pipeline. Pending renders greyed (not yet reached); the others map to their obvious icons.
  - **`status.build.termination`** — `object`, optional
    Termination records how a build container abnormally terminated (currently OOMKilled), captured from the failed pod's container state at terminal observe so the fact survives the pod being reaped. Nil unless a container terminated with a non-empty reason.
    - **`status.build.termination.container`** — `string`, optional
      Container is the build step/container that terminated (e.g. "build").
    - **`status.build.termination.exitCode`** — `integer`, optional, format `int32`
      ExitCode is the container's exit code (137 for an OOM kill).
    - **`status.build.termination.finishedAt`** — `string`, optional, format `date-time`
      FinishedAt is when the container terminated.
    - **`status.build.termination.memoryLimit`** — `string`, optional
      MemoryLimit is the memory limit that container ran with, as the Kubernetes quantity string it was configured with (e.g. "512Mi"). Read from the pod spec so it reflects the build that actually ran, not a later spec edit.
    - **`status.build.termination.reason`** — `string`, optional
      Reason is the container's terminated reason (e.g. "OOMKilled").
  - **`status.build.trigger`** — `string`, optional
    Trigger records why this build ran.
  - **`status.build.triggeredBy`** — `string`, optional
    TriggeredBy is the user who requested a manual build (empty for scheduled).
- **`status.buildHistory`** — `array<object>`, optional, maxItems `50`
  BuildHistory is a newest-first ring buffer of recent terminal builds (Jobs that ran, any result). The operator caps it to the effective keepRecent (spec.history.keepRecent or the operator-config default); CEL bounds it defensively at the keepRecent CEL cap (50).
  - **`status.buildHistory.attempts`** — `integer`, optional
  - **`status.buildHistory.commit`** — `string`, optional
    Commit is the SHA that triggered a commit-watch build (empty for other triggers), captured from RebuildCommitAnnotation at Job creation.
  - **`status.buildHistory.completionTime`** — `string`, optional, format `date-time`
  - **`status.buildHistory.failedStep`** — `string`, optional
    FailedStep names the step whose failure ended the build, when Result is Failed or Aborted.
  - **`status.buildHistory.jobName`** — `string`, optional
  - **`status.buildHistory.logsRef`** — `string`, optional
  - **`status.buildHistory.message`** — `string`, optional
  - **`status.buildHistory.phase`** — `string`, optional
    BuildPhase is the lifecycle of the current/last build pod.
  - **`status.buildHistory.podName`** — `string`, optional
    PodName is the build pod for this Job, persisted so the read-only console (which can get but not list pods) can fetch logs, and so a Loki query can be scoped by pod label.
  - **`status.buildHistory.resolvedImages`** — `map<string,string>`, optional
    ResolvedImages maps each pipeline container (shim-install/clone/setup/ fetch/build/copier) to the exact image reference the build Job was created with — digest-pinned for operator-managed toolchains. Captured at Job CREATION (like SpecHashAnnotation) so it records the build that actually ran, not a later operator-config change. Bounded by the fixed pipeline shape: 6 containers today (shim-install/clone/setup/fetch/build/ copier), 8 leaves headroom for new pipeline steps without an API bump.
  - **`status.buildHistory.result`** — `string`, optional
    BuildResult is the terminal outcome of a build pod.
  - **`status.buildHistory.startTime`** — `string`, optional, format `date-time`
  - **`status.buildHistory.steps`** — `array<object>`, optional
    Steps is the ordered per-step timeline (only applicable steps).
    - **`status.buildHistory.steps.finishedAt`** — `string`, optional, format `date-time`
      FinishedAt is when the step's container terminated. Absent while the step is still running (the console derives a live duration from StartedAt).
    - **`status.buildHistory.steps.memoryLimit`** — `string`, optional
      MemoryLimit is the memory ceiling the step's container ran with, as its Kubernetes quantity string (e.g. "2Gi"), read from the build pod SPEC so it reflects the build that actually ran. Only stamped once the container has started (Running or Terminated). Rendered next to PeakMemoryBytes so peak-vs-allocated is visible per step. Empty for the synthetic release step, a step that has not started yet, or when the pod was already reaped.
    - **`status.buildHistory.steps.message`** — `string`, optional
    - **`status.buildHistory.steps.name`** — `string`, **required**
    - **`status.buildHistory.steps.peakMemoryBytes`** — `integer`, optional, format `int64`
      PeakMemoryBytes is the phase's TRUE peak memory: the kernel-tracked cgroup high-water mark (memory.peak — the max of memory.current, the value the OOM killer compares against the limit), recorded by the shim wrapper via the container termination message when the step ends. It is the number to tune spec.pipeline.phases.<p>.memoryLimit against. 0/absent when unmeasured (shim-less steps like clone/copier, pod reaped before terminal observe, or no cgroup v2 peak available).
    - **`status.buildHistory.steps.startedAt`** — `string`, optional, format `date-time`
      StartedAt is when the step's container started, read from the kubelet's container state (Running.StartedAt or Terminated.StartedAt). Absent for a step not yet reached, the synthetic release step, or a pod reaped before terminal observe.
    - **`status.buildHistory.steps.status`** — `string`, **required**
      StepStatus is the state of one ordered step in a build's pipeline. Pending renders greyed (not yet reached); the others map to their obvious icons.
  - **`status.buildHistory.termination`** — `object`, optional
    Termination records how a build container abnormally terminated (currently OOMKilled), captured from the failed pod's container state at terminal observe so the fact survives the pod being reaped. Nil unless a container terminated with a non-empty reason.
    - **`status.buildHistory.termination.container`** — `string`, optional
      Container is the build step/container that terminated (e.g. "build").
    - **`status.buildHistory.termination.exitCode`** — `integer`, optional, format `int32`
      ExitCode is the container's exit code (137 for an OOM kill).
    - **`status.buildHistory.termination.finishedAt`** — `string`, optional, format `date-time`
      FinishedAt is when the container terminated.
    - **`status.buildHistory.termination.memoryLimit`** — `string`, optional
      MemoryLimit is the memory limit that container ran with, as the Kubernetes quantity string it was configured with (e.g. "512Mi"). Read from the pod spec so it reflects the build that actually ran, not a later spec edit.
    - **`status.buildHistory.termination.reason`** — `string`, optional
      Reason is the container's terminated reason (e.g. "OOMKilled").
  - **`status.buildHistory.trigger`** — `string`, optional
    Trigger records why this build ran.
  - **`status.buildHistory.triggeredBy`** — `string`, optional
    TriggeredBy is the user who requested a manual build (empty for scheduled).
- **`status.cleanup`** — `object`, optional
  Cleanup records the per-action cleanup state (cache prune / release prune).
  - **`status.cleanup.cache`** — `object`, optional
    CleanupActionStatus is the per-action record for one cleanup kind (cache or release prune). Phase tracks the lifecycle of the helper pod; RequestedAt mirrors the triggering annotation so the operator can detect a fresh request.
    - **`status.cleanup.cache.completedAt`** — `string`, optional, format `date-time`
      CompletedAt is when the cleanup helper last finished. Together with StartedAt it makes the prune duration observable (was: lastCompleted, a hand-formatted string — every other status timestamp is a metav1.Time).
    - **`status.cleanup.cache.message`** — `string`, optional
    - **`status.cleanup.cache.phase`** — `string`, optional
      Phase is the lifecycle of the cleanup helper: Pending|Running|Succeeded|Failed.
    - **`status.cleanup.cache.reclaimedBytes`** — `integer`, optional, format `int64`
      ReclaimedBytes is the space reclaimed by the last completed cleanup.
    - **`status.cleanup.cache.requestedAt`** — `string`, optional
      RequestedAt mirrors the cleanup request annotation's token.
    - **`status.cleanup.cache.requestedBy`** — `string`, optional
      RequestedBy is the user who requested the cleanup.
    - **`status.cleanup.cache.startedAt`** — `string`, optional, format `date-time`
      StartedAt is when the cleanup helper Job was last created.
  - **`status.cleanup.releases`** — `object`, optional
    CleanupActionStatus is the per-action record for one cleanup kind (cache or release prune). Phase tracks the lifecycle of the helper pod; RequestedAt mirrors the triggering annotation so the operator can detect a fresh request.
    - **`status.cleanup.releases.completedAt`** — `string`, optional, format `date-time`
      CompletedAt is when the cleanup helper last finished. Together with StartedAt it makes the prune duration observable (was: lastCompleted, a hand-formatted string — every other status timestamp is a metav1.Time).
    - **`status.cleanup.releases.message`** — `string`, optional
    - **`status.cleanup.releases.phase`** — `string`, optional
      Phase is the lifecycle of the cleanup helper: Pending|Running|Succeeded|Failed.
    - **`status.cleanup.releases.reclaimedBytes`** — `integer`, optional, format `int64`
      ReclaimedBytes is the space reclaimed by the last completed cleanup.
    - **`status.cleanup.releases.requestedAt`** — `string`, optional
      RequestedAt mirrors the cleanup request annotation's token.
    - **`status.cleanup.releases.requestedBy`** — `string`, optional
      RequestedBy is the user who requested the cleanup.
    - **`status.cleanup.releases.startedAt`** — `string`, optional, format `date-time`
      StartedAt is when the cleanup helper Job was last created.
- **`status.conditions`** — `array<object>`, optional
  - **`status.conditions.lastTransitionTime`** — `string`, **required**, format `date-time`
    lastTransitionTime is the last time the condition transitioned from one status to another. This should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable.
  - **`status.conditions.message`** — `string`, **required**, maxLen `32768`
    message is a human readable message indicating details about the transition. This may be an empty string.
  - **`status.conditions.observedGeneration`** — `integer`, optional, min `0`, format `int64`
    observedGeneration represents the .metadata.generation that the condition was set based upon. For instance, if .metadata.generation is currently 12, but the .status.conditions[x].observedGeneration is 9, the condition is out of date with respect to the current state of the instance.
  - **`status.conditions.reason`** — `string`, **required**, minLen `1`, maxLen `1024`, pattern `^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$`
    reason contains a programmatic identifier indicating the reason for the condition's last transition. Producers of specific condition types may define expected values and meanings for this field, and whether the values are considered a guaranteed API. The value should be a CamelCase string. This field may not be empty.
  - **`status.conditions.status`** — `string`, **required**, enum `True`, `False`, `Unknown`
    status of the condition, one of True, False, Unknown.
  - **`status.conditions.type`** — `string`, **required**, maxLen `316`, pattern `^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])$`
    type of condition in CamelCase or in foo.example.com/CamelCase.
- **`status.consecutiveScheduledFailures`** — `integer`, optional
  ConsecutiveScheduledFailures counts scheduled builds that have failed in a row: incremented on each failed Scheduled build, reset to 0 on ANY success. It drives the baker_app_consecutive_scheduled_failures gauge and the AppScheduledBuildsFailingThreshold alert (consec >= alertThreshold).
- **`status.failedBuildHistory`** — `array<object>`, optional, maxItems `50`
  FailedBuildHistory is a newest-first list of the last N FAILED builds, deduped by JobName, INDEPENDENT of BuildHistory so a burst of scheduled successes can't evict a failure needed for debugging. Each entry is trimmed to only its failed step (+ termination / memory) — enough to build a Loki query without bloating the object. Capped to the effective keepFailed (spec.history.keepFailed or the operator-config default); CEL bounds it at 50.
  - **`status.failedBuildHistory.attempts`** — `integer`, optional
  - **`status.failedBuildHistory.commit`** — `string`, optional
    Commit is the SHA that triggered a commit-watch build (empty for other triggers), captured from RebuildCommitAnnotation at Job creation.
  - **`status.failedBuildHistory.completionTime`** — `string`, optional, format `date-time`
  - **`status.failedBuildHistory.failedStep`** — `string`, optional
    FailedStep names the step whose failure ended the build, when Result is Failed or Aborted.
  - **`status.failedBuildHistory.jobName`** — `string`, optional
  - **`status.failedBuildHistory.logsRef`** — `string`, optional
  - **`status.failedBuildHistory.message`** — `string`, optional
  - **`status.failedBuildHistory.phase`** — `string`, optional
    BuildPhase is the lifecycle of the current/last build pod.
  - **`status.failedBuildHistory.podName`** — `string`, optional
    PodName is the build pod for this Job, persisted so the read-only console (which can get but not list pods) can fetch logs, and so a Loki query can be scoped by pod label.
  - **`status.failedBuildHistory.resolvedImages`** — `map<string,string>`, optional
    ResolvedImages maps each pipeline container (shim-install/clone/setup/ fetch/build/copier) to the exact image reference the build Job was created with — digest-pinned for operator-managed toolchains. Captured at Job CREATION (like SpecHashAnnotation) so it records the build that actually ran, not a later operator-config change. Bounded by the fixed pipeline shape: 6 containers today (shim-install/clone/setup/fetch/build/ copier), 8 leaves headroom for new pipeline steps without an API bump.
  - **`status.failedBuildHistory.result`** — `string`, optional
    BuildResult is the terminal outcome of a build pod.
  - **`status.failedBuildHistory.startTime`** — `string`, optional, format `date-time`
  - **`status.failedBuildHistory.steps`** — `array<object>`, optional
    Steps is the ordered per-step timeline (only applicable steps).
    - **`status.failedBuildHistory.steps.finishedAt`** — `string`, optional, format `date-time`
      FinishedAt is when the step's container terminated. Absent while the step is still running (the console derives a live duration from StartedAt).
    - **`status.failedBuildHistory.steps.memoryLimit`** — `string`, optional
      MemoryLimit is the memory ceiling the step's container ran with, as its Kubernetes quantity string (e.g. "2Gi"), read from the build pod SPEC so it reflects the build that actually ran. Only stamped once the container has started (Running or Terminated). Rendered next to PeakMemoryBytes so peak-vs-allocated is visible per step. Empty for the synthetic release step, a step that has not started yet, or when the pod was already reaped.
    - **`status.failedBuildHistory.steps.message`** — `string`, optional
    - **`status.failedBuildHistory.steps.name`** — `string`, **required**
    - **`status.failedBuildHistory.steps.peakMemoryBytes`** — `integer`, optional, format `int64`
      PeakMemoryBytes is the phase's TRUE peak memory: the kernel-tracked cgroup high-water mark (memory.peak — the max of memory.current, the value the OOM killer compares against the limit), recorded by the shim wrapper via the container termination message when the step ends. It is the number to tune spec.pipeline.phases.<p>.memoryLimit against. 0/absent when unmeasured (shim-less steps like clone/copier, pod reaped before terminal observe, or no cgroup v2 peak available).
    - **`status.failedBuildHistory.steps.startedAt`** — `string`, optional, format `date-time`
      StartedAt is when the step's container started, read from the kubelet's container state (Running.StartedAt or Terminated.StartedAt). Absent for a step not yet reached, the synthetic release step, or a pod reaped before terminal observe.
    - **`status.failedBuildHistory.steps.status`** — `string`, **required**
      StepStatus is the state of one ordered step in a build's pipeline. Pending renders greyed (not yet reached); the others map to their obvious icons.
  - **`status.failedBuildHistory.termination`** — `object`, optional
    Termination records how a build container abnormally terminated (currently OOMKilled), captured from the failed pod's container state at terminal observe so the fact survives the pod being reaped. Nil unless a container terminated with a non-empty reason.
    - **`status.failedBuildHistory.termination.container`** — `string`, optional
      Container is the build step/container that terminated (e.g. "build").
    - **`status.failedBuildHistory.termination.exitCode`** — `integer`, optional, format `int32`
      ExitCode is the container's exit code (137 for an OOM kill).
    - **`status.failedBuildHistory.termination.finishedAt`** — `string`, optional, format `date-time`
      FinishedAt is when the container terminated.
    - **`status.failedBuildHistory.termination.memoryLimit`** — `string`, optional
      MemoryLimit is the memory limit that container ran with, as the Kubernetes quantity string it was configured with (e.g. "512Mi"). Read from the pod spec so it reflects the build that actually ran, not a later spec edit.
    - **`status.failedBuildHistory.termination.reason`** — `string`, optional
      Reason is the container's terminated reason (e.g. "OOMKilled").
  - **`status.failedBuildHistory.trigger`** — `string`, optional
    Trigger records why this build ran.
  - **`status.failedBuildHistory.triggeredBy`** — `string`, optional
    TriggeredBy is the user who requested a manual build (empty for scheduled).
- **`status.lastBuildTime`** — `string`, optional, format `date-time`
- **`status.lastBuiltSpecHash`** — `string`, optional
- **`status.lastManualTrigger`** — `object`, optional
  ManualTrigger records the last manual rebuild request.
  - **`status.lastManualTrigger.time`** — `string`, optional, format `date-time`
  - **`status.lastManualTrigger.triggeredBy`** — `string`, optional
- **`status.lastProcessedRebuild`** — `string`, optional
- **`status.lastSuccessfulBuildTime`** — `string`, optional, format `date-time`
- **`status.nodeName`** — `string`, optional
- **`status.observedGeneration`** — `integer`, optional, format `int64`
- **`status.phase`** — `string`, optional
  Phase is the derived top-level lifecycle phase (computed from conditions).
- **`status.release`** — `object`, optional
  ReleaseStatus tracks the served release pointers.
  - **`status.release.current`** — `string`, optional
  - **`status.release.previous`** — `string`, optional
  - **`status.release.servingSince`** — `string`, optional, format `date-time`
- **`status.specStale`** — `boolean`, optional
- **`status.storage`** — `object`, optional
  StorageStatus records the most recent du measurement.
  - **`status.storage.capacities`** — `map<string,integer>`, optional
    Capacities maps each PVC-backed volume (cache / dataCache / output) to its provisioned capacity in bytes, read from the bound PVC's status.capacity. The console draws the storage fill bars against these when no explicit spec.storage cap applies (outputTotal in particular is physically bounded by the output PVC). Bounded by the fixed volume set; 8 leaves headroom, mirroring resolvedImages.
  - **`status.storage.lastRunDeltas`** — `map<string,integer>`, optional
  - **`status.storage.measuredAt`** — `string`, optional, format `date-time`
  - **`status.storage.releaseCount`** — `integer`, optional, format `int64`
    ReleaseCount is the number of release directories retained on the output PVC, counted by the copier AFTER its retention sweep and flip (so it is the real on-disk count, not spec.keepReleases). 0/absent when no copier has reported yet.
  - **`status.storage.sizes`** — `map<string,integer>`, optional
  - **`status.storage.thresholdState`** — `string`, optional
- **`status.url`** — `string`, optional
