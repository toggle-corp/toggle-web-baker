# Changelog

## [0.1.1](https://github.com/toggle-corp/toggle-web-baker/compare/0.1.0..0.1.1) - 2026-07-04
### Changes:

#### 🚀  Features

- *(app)* Add literal-only envMap phase env field - ([6e54055](https://github.com/toggle-corp/toggle-web-baker/commit/6e54055b1abf07235d14277492b2d4560bf971fc))

#### 🧪 Testing

- *(app)* Cover envMap fold + setup/fetch injection at controller level - ([634258d](https://github.com/toggle-corp/toggle-web-baker/commit/634258de6da340b29630f1cf90c81f119e7f7428))

#### ⚙️ Miscellaneous Tasks

- *(app)* Satisfy lint for envMap change - ([6e511dc](https://github.com/toggle-corp/toggle-web-baker/commit/6e511dc39f5b3c185db52f43a856ca7bfc896fd2))


## [0.1.0] - 2026-07-03
### Changes:

#### 🚀  Features

- *(api)* [**breaking**] Require pipeline.packageManager; add setup.skip - ([757c354](https://github.com/toggle-corp/toggle-web-baker/commit/757c3546a2a5d50dea4cad2ec05aa58641d27fe2))
- *(api)* [**breaking**] Status observability — resolvedImages + cleanup timestamps - ([cf738d4](https://github.com/toggle-corp/toggle-web-baker/commit/cf738d4823e83b1344472d9160b1879e29878af9))
- *(api)* Printer columns for build result, storage state, last success - ([183b686](https://github.com/toggle-corp/toggle-web-baker/commit/183b6869fe3bb14ef68698054e9304c56c6432cf))
- *(api)* Add spec.group informational console-grouping label - ([b1cfc3d](https://github.com/toggle-corp/toggle-web-baker/commit/b1cfc3d2047318d9553aabe8a89236a5ba0dd106))
- *(api)* Add BuildTermination status field for OOMKilled builds - ([4da95b6](https://github.com/toggle-corp/toggle-web-baker/commit/4da95b63d6acb53fb939942cfccf36e2c06d09d5))
- *(api)* [**breaking**] Consolidate build env into build.env; move outputDir under build - ([0eb84cb](https://github.com/toggle-corp/toggle-web-baker/commit/0eb84cb827229c21f7e6408c471ab9679aeaf9c7))
- *(api)* Add spec.nodeVersion field + build-image CEL rule - ([6a02619](https://github.com/toggle-corp/toggle-web-baker/commit/6a0261955d27955c3844fcc3be23db2ce97c54f9))
- *(api)* Add BuildStep timeline, build history, podName & Aborted to status - ([ea57299](https://github.com/toggle-corp/toggle-web-baker/commit/ea57299f579b67e02f1aad460beda62b9588eb35))
- *(api,cleanup-image)* Cleanup annotations, status, and MODE switch - ([dce49ef](https://github.com/toggle-corp/toggle-web-baker/commit/dce49eff8e923dfc874ee44b99de560c1951e153))
- *(api,operator)* Opt-in scheduledBuilds + watchCommits trigger structs - ([a895859](https://github.com/toggle-corp/toggle-web-baker/commit/a89585995514db1213427ca24dd07bfae6d32036))
- *(api,operator)* Per-step times/limits, release count, PVC capacities - ([4d9b670](https://github.com/toggle-corp/toggle-web-baker/commit/4d9b670e8af98b705c5f2d8f382ed1d95a9c2177))
- *(build)* Record TRUE per-phase peak memory via a cgroup shim - ([a93139c](https://github.com/toggle-corp/toggle-web-baker/commit/a93139cf11b8069d2182166eaa4836db08e6ab10))
- *(chart)* Operator.gitAuth values — existingSecret XOR chart-created credential - ([ca83ad1](https://github.com/toggle-corp/toggle-web-baker/commit/ca83ad1b5488c6a2d18bd13d9b96cb5c19b70ef0))
- *(clock)* Watch mode — trigger rebuilds on new commits via ls-remote poll - ([8b36b00](https://github.com/toggle-corp/toggle-web-baker/commit/8b36b009d131ee193912550b765408f63ecef07c))
- *(clone)* Fall back to anonymous https for SSH GitHub URLs - ([c4131ca](https://github.com/toggle-corp/toggle-web-baker/commit/c4131ca10051e912a39a93771a3a980235db8549))
- *(config)* Chart-owned defaultSetupCommands with compiled-in fallbacks - ([fdc3afd](https://github.com/toggle-corp/toggle-web-baker/commit/fdc3afd257e942822548da52a4b0c9e39a1cb904))
- *(console)* Hint at operator-default setup on the manifest page - ([0b8d2d9](https://github.com/toggle-corp/toggle-web-baker/commit/0b8d2d9085464c406d669815d9d1f8713e0cf15b))
- *(console)* Read-only App manifest page - ([e00308f](https://github.com/toggle-corp/toggle-web-baker/commit/e00308fbe630657da90935f0d0d09975e7327a3f))
- *(console)* Serve during cache warm-up + staleness banner - ([4e63efc](https://github.com/toggle-corp/toggle-web-baker/commit/4e63efc2f549e34c24f927ba40e185916383c043))
- *(console)* Storage totals — list header aggregate + per-app column - ([2b5ffe7](https://github.com/toggle-corp/toggle-web-baker/commit/2b5ffe7af9eaf6913377c27fe745075a46819148))
- *(console)* Paginate the app list (50/page) - ([5669c52](https://github.com/toggle-corp/toggle-web-baker/commit/5669c520aee3383836c2baaad91c019b06cd0a98))
- *(console)* Server-side search on the app list - ([8ce1c4e](https://github.com/toggle-corp/toggle-web-baker/commit/8ce1c4e53c03405875444e6e0018cd895b225fad))
- *(console)* Commit-trigger badge, linked SHAs, trigger-config display - ([eb87756](https://github.com/toggle-corp/toggle-web-baker/commit/eb87756ced5f65bf46fb1d02564d236f7401e0a0))
- *(console)* Sentry middleware with panic recovery and 5xx capture - ([b4a969c](https://github.com/toggle-corp/toggle-web-baker/commit/b4a969c0d8250dc8fdc6fe45de9fd96d8909fc81))
- *(console)* Build details card, step durations, capacity bars, log fixes - ([25db2b1](https://github.com/toggle-corp/toggle-web-baker/commit/25db2b1aa03daa889089953c1df63e25347cc5ba))
- *(console)* Cockpit layout, group/status filters, e2e-verified UX overhaul - ([06cfa2f](https://github.com/toggle-corp/toggle-web-baker/commit/06cfa2f26dc1fe2826897fe69523e48f55303b2a))
- *(console)* Surface OOMKilled builds in the UI - ([4af8dd3](https://github.com/toggle-corp/toggle-web-baker/commit/4af8dd3030685caffbffa7783da3a12b2ebae60b))
- *(console)* Unify recent-build logs into the single log pane - ([936ef93](https://github.com/toggle-corp/toggle-web-baker/commit/936ef93f7c3208246916cd151c3b3865c521d1ef))
- *(console)* Colorize logs, fix log-box height, gate follow toggle - ([56389c8](https://github.com/toggle-corp/toggle-web-baker/commit/56389c88d13fc253a5175b1b0e2021d25624aea8))
- *(console)* Render 'Trigger · user' via Build.TriggerLabel - ([d74be2f](https://github.com/toggle-corp/toggle-web-baker/commit/d74be2f56de2032274aceca318e2d3f9bc82670d))
- *(console)* Localize all timestamps to viewer timezone - ([c8122b5](https://github.com/toggle-corp/toggle-web-baker/commit/c8122b5bb9e3bbce0b56087031d441079fa4ebb0))
- *(console)* Derive Next scheduled from spec.schedule - ([71c465c](https://github.com/toggle-corp/toggle-web-baker/commit/71c465cf2b93fb885e800fd7d8c892a6c0784294))
- *(console)* OutputTotal row (no bar) + friendly storage labels - ([b9cf71c](https://github.com/toggle-corp/toggle-web-baker/commit/b9cf71cd5c1b89c4c353554cb4e78b50691b7212))
- *(console)* Cache + release cleanup buttons in storage card - ([71042b6](https://github.com/toggle-corp/toggle-web-baker/commit/71042b6446f090956f63c83ff1e766e1c689b7e9))
- *(console)* Live build-pod CPU/memory in build flow card - ([20dfea1](https://github.com/toggle-corp/toggle-web-baker/commit/20dfea19c91bfd7ebea6386c949cec4b448330e2))
- *(console)* Follow active build step in log pane - ([a283b8e](https://github.com/toggle-corp/toggle-web-baker/commit/a283b8e67302cfba175dd25794cf2a60f24b575f))
- *(console)* Flow strip, build history, storage bars & log pane - ([f6c62b4](https://github.com/toggle-corp/toggle-web-baker/commit/f6c62b45868fec93a5cb8f011793eae4012927ad))
- *(console)* View-model mapping, humanizers & Loki log client - ([a200ecf](https://github.com/toggle-corp/toggle-web-baker/commit/a200ecfd02093fe5f817f7043c1a80b751b3f788))
- *(console)* Follow system theme with System/Light/Dark toggle - ([01b0f64](https://github.com/toggle-corp/toggle-web-baker/commit/01b0f64aa00f3f8d20cf92446fe7d3587af76d4b))
- *(console)* Add logout link and themed signed-out page - ([8172a08](https://github.com/toggle-corp/toggle-web-baker/commit/8172a080d701c9643127645d8f74731faed23393))
- *(console)* Expose /healthz for external uptime monitoring - ([7803a6c](https://github.com/toggle-corp/toggle-web-baker/commit/7803a6c77b24d999a65f3264b41c311952b844dc))
- *(console/view)* Storage total + category-breakdown helpers - ([978e352](https://github.com/toggle-corp/toggle-web-baker/commit/978e35267a476e48cc56e12ad184cc09c94f00fe))
- *(controller)* Detect OOMKilled builds and record termination - ([235400f](https://github.com/toggle-corp/toggle-web-baker/commit/235400f735afad7bb7fd42541c5d5bf507ba7782))
- *(copier)* Report the retained release count in the termination JSON - ([cc12c62](https://github.com/toggle-corp/toggle-web-baker/commit/cc12c62381d11fa9307986393cdae0f3ca2841d5))
- *(copier)* Emit sizes.outputTotal; move source out of sizes map - ([368d64d](https://github.com/toggle-corp/toggle-web-baker/commit/368d64d328b9ebfbb2030c7a42a0cbbe3b00e75e))
- *(domain)* Add NodeImage type and per-phase resolution - ([93451f8](https://github.com/toggle-corp/toggle-web-baker/commit/93451f81b4736c1b3ca06213769bda8c73ca8624))
- *(domain)* Include nodeVersion in the build spec hash - ([9ebe680](https://github.com/toggle-corp/toggle-web-baker/commit/9ebe68098779b9a8a6787d6494a69ca6d3bc1bce))
- *(domain)* Build-trigger predicate (single active build, sole creator) - ([92a9fae](https://github.com/toggle-corp/toggle-web-baker/commit/92a9fae0819ec643069ce7b228358136477031ba))
- *(domain)* Build-relevant-spec hash + staleness detection - ([4f421ef](https://github.com/toggle-corp/toggle-web-baker/commit/4f421efc1ba7c7bab3bc50f23359ed18872e3964))
- *(domain)* Registry allowlist check (reconcile-time, fail-closed) - ([4d8125f](https://github.com/toggle-corp/toggle-web-baker/commit/4d8125f6b3014767a9bfbb89e6a2979cfebaf065))
- *(domain)* Storage threshold ordering validation - ([064818b](https://github.com/toggle-corp/toggle-web-baker/commit/064818be60735d1cc096a6206b1d706afc0da9b8))
- *(helm)* DefaultSchedule/defaultWatchInterval values; sample opts into both triggers - ([8fe57f9](https://github.com/toggle-corp/toggle-web-baker/commit/8fe57f942ee40def9de567ac508de3cf323e57f3))
- *(helm)* Opt-in monitoring block — metrics Service, ServiceMonitor, PrometheusRule - ([5317b7d](https://github.com/toggle-corp/toggle-web-baker/commit/5317b7dfe02ddf3aff32a1652adc66783383214e))
- *(helm)* Sentry.{dsn,environment} values rendered on both deployments - ([8ee0d56](https://github.com/toggle-corp/toggle-web-baker/commit/8ee0d56a8d948f600726cdcad92a041100e90c5f))
- *(helm)* Render operator config as a mounted ConfigMap - ([c367373](https://github.com/toggle-corp/toggle-web-baker/commit/c3673731adbaba648d3c901326a763d2cb7b3400))
- *(helm)* Ship operator.nodeImages map + --node-images flag - ([47dc23c](https://github.com/toggle-corp/toggle-web-baker/commit/47dc23c5ada5da773c55dfd5c81497b51f5693d0))
- *(images)* Credential mount support in clock watch mode; unconditional ssh->https rewrite - ([30362ac](https://github.com/toggle-corp/toggle-web-baker/commit/30362acaa67de6dded520324b867c2abc5926fdd))
- *(images)* Content-hash tag helper + image-set drift guard - ([3d06f48](https://github.com/toggle-corp/toggle-web-baker/commit/3d06f481bb54114aa73a898b5d9cb2f836694b6e))
- *(images)* Add node18/node24 managed toolchain images - ([fab4dc7](https://github.com/toggle-corp/toggle-web-baker/commit/fab4dc7ee7fa19efcf6bbc55e3d40d616682e898))
- *(node-images)* Add build-base + python3 for node-gyp - ([52a5237](https://github.com/toggle-corp/toggle-web-baker/commit/52a52373a73b4e0c008d86ad50535acc60ed5bdf))
- *(observability)* Sentry reporter, rate limiter, filters, zap core - ([fa3dc15](https://github.com/toggle-corp/toggle-web-baker/commit/fa3dc15f446b3aaf9b2e52569f5f9e74b6d908b8))
- *(operator)* Inject default setup command; honor setup.skip - ([e24d38e](https://github.com/toggle-corp/toggle-web-baker/commit/e24d38e2d0f91ba2ca7d9539757c2361f3db450d))
- *(operator)* Frontendapp_* Prometheus metrics for alerting - ([9b59b25](https://github.com/toggle-corp/toggle-web-baker/commit/9b59b2596956d074f6c1c60f01a5b3e121522682))
- *(operator)* Wire Sentry into main — zap tee, reporter, flush on exit - ([5f98638](https://github.com/toggle-corp/toggle-web-baker/commit/5f98638dbdb4100ddb4a26c650b56d4da9a40cca))
- *(operator)* Report platform-fault terminal failures to Sentry - ([268c82f](https://github.com/toggle-corp/toggle-web-baker/commit/268c82f56ac87b893fe09a35c79dcc5bc11defd1))
- *(operator)* Resolve nodeVersion to image/UID/HOME + reject unknown versions - ([7cfea38](https://github.com/toggle-corp/toggle-web-baker/commit/7cfea38dfbc7d7044036876a006962c1e88566ee))
- *(operator)* Add -node-images flag and NodeImages config - ([564b2a1](https://github.com/toggle-corp/toggle-web-baker/commit/564b2a1dddab769527f4840e26f33d6a7b0a8cfe))
- *(operator)* Attribute manual rebuilds (BuildStatus.triggeredBy + lastManualTrigger) - ([99afcfa](https://github.com/toggle-corp/toggle-web-baker/commit/99afcfa7f548bc74820ffebd484a333d4ec9e6c8))
- *(operator)* Carry sizes.outputTotal; prune stale source key on merge - ([9db8999](https://github.com/toggle-corp/toggle-web-baker/commit/9db8999ebd5bf92bc42f3c04def7899d7b4de720))
- *(operator)* Run cache/release cleanup Jobs on annotation request - ([a558e94](https://github.com/toggle-corp/toggle-web-baker/commit/a558e94afbbf896b2bad97bf2d0bfcff974b9325))
- *(operator)* Measure cache+dataCache PVCs via du Jobs, compute thresholdState - ([f2bce52](https://github.com/toggle-corp/toggle-web-baker/commit/f2bce52a3b00dad596e91da6e80e1b5eaea02328))
- *(operator)* Per-step timeline, build history, trigger & pod-watch - ([d69ba98](https://github.com/toggle-corp/toggle-web-baker/commit/d69ba98c94f9ce8baea210b8d6c3b7a22beb3f39))
- *(operator)* Make spec.submodules actually control recursion - ([d9e27b5](https://github.com/toggle-corp/toggle-web-baker/commit/d9e27b5ba3441e3e90dd92796ca7bceaa660aa59))
- *(operator)* Validate FrontendApp at apply time (CEL) + envtest - ([11b05dc](https://github.com/toggle-corp/toggle-web-baker/commit/11b05dc3c240ba1f5f634b76cb2c920bd98e43de))
- *(release)* Pin node image content-hash tags into values.yaml - ([19b9176](https://github.com/toggle-corp/toggle-web-baker/commit/19b9176549c42a23d1b0c57c6c17a6e6a33eae15))
- *(shim)* Echo the exec'd phase command as the first stdout line - ([950fb59](https://github.com/toggle-corp/toggle-web-baker/commit/950fb59444c6f76df6da02a20f2febe776c17002))
- Spec.repoAuth override + per-app synced global git credential - ([3a6c5de](https://github.com/toggle-corp/toggle-web-baker/commit/3a6c5de3eb1ca1b70299a8d814a7ede0d3b4b478))
- Operator-global git credential — domain host allowlist, config gitAuth, startup check - ([35ccb79](https://github.com/toggle-corp/toggle-web-baker/commit/35ccb79b1a07546c3449638113d64fab544d4064))
- [**breaking**] Rename CRD kind FrontendApp -> App - ([a6eb927](https://github.com/toggle-corp/toggle-web-baker/commit/a6eb9271271d7afb0b267cade630270da31e25a1))
- Release.sh wrapper (fugit) + CHANGELOG seed - ([88f89e9](https://github.com/toggle-corp/toggle-web-baker/commit/88f89e933e1e8517b471b59ac8be20f8bca46da2))
- Helm chart (operator + optional console) - ([daa1ade](https://github.com/toggle-corp/toggle-web-baker/commit/daa1adef957356f95d65511d7fb48794d2e0e6b9))
- FrontendApp Kubernetes operator - ([0ce5537](https://github.com/toggle-corp/toggle-web-baker/commit/0ce5537931eae356c20bb12425ee99bcc574791d))

#### 🐛 Bug Fixes

- *(api)* Admission-validate memoryLimit, storage bounds, keepReleases, repo shape - ([6ce743c](https://github.com/toggle-corp/toggle-web-baker/commit/6ce743c8a84f51556ce33597a11f92f48ba8d359))
- *(api)* [**breaking**] Pipeline.timeout as *Duration with positive-duration CEL - ([2c63de4](https://github.com/toggle-corp/toggle-web-baker/commit/2c63de4881af912a477afb1aede6195c1974613b))
- *(api)* Segment-based outputDir validation; drop dead build-args ConfigMap - ([9ba3535](https://github.com/toggle-corp/toggle-web-baker/commit/9ba3535e72269ba9e4c3e8f7cf794376765ff6d3))
- *(build)* Run clone as the build-phase UID so in-tree codegen works - ([f83dccf](https://github.com/toggle-corp/toggle-web-baker/commit/f83dccf13b66ae20624939725a87427192cb7c21))
- *(ci)* Green the pipeline (docker push bool, lint action v7, snapshots) - ([1544de7](https://github.com/toggle-corp/toggle-web-baker/commit/1544de721304918995da5ddbff3da47236fb6e1d))
- *(clone)* Fetch top-level submodules only, not recursively - ([0cbdf69](https://github.com/toggle-corp/toggle-web-baker/commit/0cbdf691df3d5a10b2996a962cf9432d6b569c61))
- *(config)* Code-review fixes for operator config loader - ([e632e2d](https://github.com/toggle-corp/toggle-web-baker/commit/e632e2d33a929b850f4748c399ce6adb014020da))
- *(console)* UX-review fixes across list/detail/error pages - ([3662616](https://github.com/toggle-corp/toggle-web-baker/commit/366261639d562895942ff4f29bdb402ecb99adcd))
- *(console)* Review fixes — filtered count, per-field search, Close race, single-pass storage - ([33c7513](https://github.com/toggle-corp/toggle-web-baker/commit/33c7513f48895bcf9225d70971f2cd4caedf0240))
- *(console)* Review fixes — panic semantics, 5xx rate limit, dedup - ([257efd7](https://github.com/toggle-corp/toggle-web-baker/commit/257efd78226b4b06861a84668967d0a8c7e31f5c))
- *(console)* Read live build usage from kubelet stats, not metrics.k8s.io - ([df55194](https://github.com/toggle-corp/toggle-web-baker/commit/df55194191db7f4208d2cc43b4a98aaa7dd18c2c))
- *(console)* Address code-review on log-pane UI - ([07318d8](https://github.com/toggle-corp/toggle-web-baker/commit/07318d83ba1b0f56e6bb6e08f654fdff96eec017))
- *(console)* Address code-review on timestamp/schedule changes - ([9d39764](https://github.com/toggle-corp/toggle-web-baker/commit/9d397647e2c2de8591e6dc4d657bc35e1d666246))
- *(console)* Satisfy golangci-lint (errcheck + staticcheck SA1012) - ([4d95f24](https://github.com/toggle-corp/toggle-web-baker/commit/4d95f244aad0dd584a4af20b934e1ae452e23fae))
- *(console)* Redirect unauthenticated users to GitHub via oauth2-proxy upstream mode - ([a482a43](https://github.com/toggle-corp/toggle-web-baker/commit/a482a43242a4a03ed26a99baeb99a9a7d0c9c600))
- *(copier)* Emit release.current + sizes map to match operator contract - ([99879fe](https://github.com/toggle-corp/toggle-web-baker/commit/99879fe1cfe0b10c06de98585dca45455cdc4b96))
- *(copier)* Allow leading-dash filenames in the source tree - ([bb06c03](https://github.com/toggle-corp/toggle-web-baker/commit/bb06c033198f0279d47f66b9df9e72c1b0dafdbc))
- *(domain)* Close review findings (transitivity, nil/empty hash, allowlist boundary) - ([d023d58](https://github.com/toggle-corp/toggle-web-baker/commit/d023d586a337b435b741c7db7d222c156567089d))
- *(e2e)* Load the clock and shim images in e2e-local - ([7a08919](https://github.com/toggle-corp/toggle-web-baker/commit/7a089192d47cae44f3e01743ecf210bf273472ab))
- *(e2e)* Make kind smoke pass end-to-end; non-root build pipeline - ([5257d92](https://github.com/toggle-corp/toggle-web-baker/commit/5257d92f6f1c4f5ce32cdb5724cbb21eb8b54a51))
- *(e2e)* Fail fast on failed build; review cleanups - ([b365a6f](https://github.com/toggle-corp/toggle-web-baker/commit/b365a6fb11c5e5e0ea28cf7414ab2e4592ec1c14))
- *(lint)* Drop redundant .Duration selector on metav1.Duration - ([1b68880](https://github.com/toggle-corp/toggle-web-baker/commit/1b688803f6a1d6cdf1f1d546dd2a9cf2c8167c7f))
- *(metrics)* Review fixes — alert reliability, single-source enums, races - ([1b51028](https://github.com/toggle-corp/toggle-web-baker/commit/1b51028b3cae7955705f2d966c7609cf6759c805))
- *(node-images)* Disable corepack auto-pin (COREPACK_ENABLE_AUTO_PIN=0) - ([e181202](https://github.com/toggle-corp/toggle-web-baker/commit/e181202b67eefe871cfa1b7fa50c83a8b675b994))
- *(node-images)* Address code-review findings - ([26c4c86](https://github.com/toggle-corp/toggle-web-baker/commit/26c4c8673e732ef7ff86af32e6d7eed565bfdd89))
- *(operator)* Review fixes — fingerprint keys, copier OOM, DSN resilience - ([62345a2](https://github.com/toggle-corp/toggle-web-baker/commit/62345a2bc67e264f898b96b25a1ccadcddf6f002))
- *(operator)* Stop the child-rewrite hot loop and job-result loss race - ([e8078b8](https://github.com/toggle-corp/toggle-web-baker/commit/e8078b8b2797f6973c5ee969ed827d670e4907f2))
- *(operator)* Repair the never-working clock CronJob - ([5c4da42](https://github.com/toggle-corp/toggle-web-baker/commit/5c4da429ca030fedd8684c9abd8687f672f25d77))
- *(operator)* Refresh storage sizes after a prune - ([aefbd54](https://github.com/toggle-corp/toggle-web-baker/commit/aefbd54527fae478c5beaaf9d1e73a46dcf0abf2))
- *(operator)* Run cleanup as root with DAC_OVERRIDE+FOWNER - ([392116b](https://github.com/toggle-corp/toggle-web-baker/commit/392116b95ccc1a634d6df6b99f1c195fc875b35f))
- *(operator)* Mark release step done on copier success - ([246b6bb](https://github.com/toggle-corp/toggle-web-baker/commit/246b6bbf37c2a8b9b34202da532813652635f98f))
- *(operator)* Drive clone/copier via env, no-op optional phases - ([8945e31](https://github.com/toggle-corp/toggle-web-baker/commit/8945e31c658bd57f83678a1b4d94b57d468b6afc))
- *(operator)* Stop wiping immutable PVC/Service spec on reconcile - ([c26f1bb](https://github.com/toggle-corp/toggle-web-baker/commit/c26f1bbc393730987502da950d8881fafd1565fe))
- *(operator)* Resolve 10 code-review findings in FrontendApp operator - ([02ace71](https://github.com/toggle-corp/toggle-web-baker/commit/02ace717138a130af59b65a0fb95db0a0349209e))
- *(rbac)* Grant operator leader-election lease access - ([7111a99](https://github.com/toggle-corp/toggle-web-baker/commit/7111a99296c6cbe8829793bdb41feb3d67c265ee))
- *(review)* Anchor resolvedImages test to both container lists; explain MaxProperties bound - ([9c5d593](https://github.com/toggle-corp/toggle-web-baker/commit/9c5d593ef749da582a85e8888ae1c2950b2b9ab0))
- *(review)* Correct node image tags in release body; harden drift guard - ([18aecbd](https://github.com/toggle-corp/toggle-web-baker/commit/18aecbd5f4720e49ded4f7dbf64c9ee3edbe2b85))
- *(review)* Non-positive pipeline.timeout fallback; relocate build-command rule; comment/console path fixes - ([29420a5](https://github.com/toggle-corp/toggle-web-baker/commit/29420a50cc0f955e4ca5290d9d4459a9ebc100b4))
- *(review)* Harden log container picker, step fallback & pod watch - ([1fc0df9](https://github.com/toggle-corp/toggle-web-baker/commit/1fc0df9aa7fe13a7b00bfd910408a6de41e77246))
- Code-review fixes for the default-setup-command feature - ([f1e070d](https://github.com/toggle-corp/toggle-web-baker/commit/f1e070d58bdde02afc2b82eb4ae3eacc363689a3))
- Code-review fixes for the git-credential feature - ([0b3619e](https://github.com/toggle-corp/toggle-web-baker/commit/0b3619ef826acdc69864963d692f222f81c3244a))
- Code-review fixes — CEL quote corruption, credential mask, clock env - ([5ebb5df](https://github.com/toggle-corp/toggle-web-baker/commit/5ebb5dffad8571934eec5e66860d1dabff14a314))
- Code-review fixes for the watchCommits feature - ([cab99ed](https://github.com/toggle-corp/toggle-web-baker/commit/cab99ed2e94e4e7e59b559922531c515951d67ae))
- Address code-review findings on storage sizes - ([2f3a6b4](https://github.com/toggle-corp/toggle-web-baker/commit/2f3a6b489fe959d3f2055f3313c9d7bf52e8548f))
- Address code-review findings on cleanup + metrics + log-follow - ([4dbe186](https://github.com/toggle-corp/toggle-web-baker/commit/4dbe186f0735e5851f4bbf58fd1b05bcd3bc6aa0))
- Resolve code-review findings (release-blocking) - ([7c689b3](https://github.com/toggle-corp/toggle-web-baker/commit/7c689b37c936136f0ac09fdd593f468e396be5bb))

#### 🚜 Refactor

- *(api)* Declarative outputDir default "dist" - ([c5d26d3](https://github.com/toggle-corp/toggle-web-baker/commit/c5d26d37a950306bd42758a5a17234272193a95b))
- *(api)* [**breaking**] Remove unused storage.node and status.nextScheduledBuildTime - ([cd75be8](https://github.com/toggle-corp/toggle-web-baker/commit/cd75be8e21162c2b36d52c2d0af482b0fa342697))
- *(api)* [**breaking**] Group build config under spec.pipeline; timeout as duration - ([9074f71](https://github.com/toggle-corp/toggle-web-baker/commit/9074f71eea364a188ac683f9560d5075b4558ae6))
- *(api)* [**breaking**] Remove dataFreshness from FrontendApp status - ([9210147](https://github.com/toggle-corp/toggle-web-baker/commit/9210147e6c8d8bcbc237968eb97af4f1636d858d))
- *(console)* Remove Data freshness from detail view - ([7c7e87a](https://github.com/toggle-corp/toggle-web-baker/commit/7c7e87afc16b4b9fe33b9e469cd1c9487d7b957b))
- *(console)* Address code-review findings on the oauth2 rewiring - ([f10a7fd](https://github.com/toggle-corp/toggle-web-baker/commit/f10a7fd4b92bb45f549ad798915adb6620a6cd59))
- *(controller)* Single-source OOM attribution on failedStep - ([78def2d](https://github.com/toggle-corp/toggle-web-baker/commit/78def2d46c1ff05e31005a0147e18427d9c7afa4))
- *(copier)* Drop now-unused PHASE_ENV default - ([02a0845](https://github.com/toggle-corp/toggle-web-baker/commit/02a08452508d17b0906119bbe0f90a48e4356ee8))
- *(copier)* Stop emitting dataFreshness; drop DATA_LAST_MODIFIED - ([b3ed314](https://github.com/toggle-corp/toggle-web-baker/commit/b3ed3140b954989af4ea658674b4c8032b4faec7))
- *(frontendapp)* Per-phase memoryLimit + operator-owned resource defaults - ([64cc8c6](https://github.com/toggle-corp/toggle-web-baker/commit/64cc8c60efda43ec715e4ee1cf80f91e18bd8a19))
- *(helm)* Dedupe SENTRY_* env into a sentryEnv named template - ([194109f](https://github.com/toggle-corp/toggle-web-baker/commit/194109f237685eadfdeb6c7b89eb4230c59f6a8d))
- *(operator)* Drop dataFreshness from copier status ingest - ([18aa939](https://github.com/toggle-corp/toggle-web-baker/commit/18aa93912d09e9ab5ff6e7bcb3128822f851bfc8))
- *(operator)* Hoist classifyTrigger in startBuild - ([290d984](https://github.com/toggle-corp/toggle-web-baker/commit/290d984865712719a01cb6edb316c89798951cb2))
- *(operator)* Least-privilege cleanup, root only as fallback - ([cca44f0](https://github.com/toggle-corp/toggle-web-baker/commit/cca44f0939abb0115ea3f345deb651ba75f1784a))
- Flat toggle-web-baker-<image> registry scheme - ([dbaf872](https://github.com/toggle-corp/toggle-web-baker/commit/dbaf8729beeebb00798dc5646865aed3833cfd06))

#### 📚 Documentation

- *(agents)* Point to gitignored AGENTS.local.md; drop tests-sample ignore - ([275387a](https://github.com/toggle-corp/toggle-web-baker/commit/275387abdeca53f3ab30cc20fcc8372972dffc6c))
- *(agents)* Require golangci-lint on both modules before pushing - ([85c99c0](https://github.com/toggle-corp/toggle-web-baker/commit/85c99c05ffe9af582dbc591fed3db927ad68eecd))
- *(agents)* Require helm snapshot check before every commit - ([7d02a0a](https://github.com/toggle-corp/toggle-web-baker/commit/7d02a0a8bc28fbea7f0f75e7ba94ad3ff4227db8))
- *(sample)* Migrate sample + security invariants to build.env / build.outputDir - ([a99cdd7](https://github.com/toggle-corp/toggle-web-baker/commit/a99cdd712544910243cb19455bf6fd177a7e28db))
- Sentry reporting policy and setup - ([474e1be](https://github.com/toggle-corp/toggle-web-baker/commit/474e1becf0de90fb0799a62b03b3664c97bc10dc))
- ClusterCIDRs placeholder note + helm upgrade --install - ([2549e67](https://github.com/toggle-corp/toggle-web-baker/commit/2549e6717cfdda199471ab6c806529c78b05d20e))
- Use toggle-baker-system as the install namespace - ([5a4bf25](https://github.com/toggle-corp/toggle-web-baker/commit/5a4bf252d5ac2fc61a8f8851f41ca7ef408af4fd))
- Note chart ships helper images by tag, not digest - ([680d334](https://github.com/toggle-corp/toggle-web-baker/commit/680d334d3aa90b2e05d702b59d9ecf73c529cd2c))
- Operator security invariants (updated with grilling resolutions) - ([cf8a6d7](https://github.com/toggle-corp/toggle-web-baker/commit/cf8a6d75788fd1bbb428f09af631330add23425e))

#### ⚡ Performance

- *(console)* Serve app list from an informer cache - ([7c709a4](https://github.com/toggle-corp/toggle-web-baker/commit/7c709a44b63b5ba2df6d0cef112920250e0b2870))

#### 🧪 Testing

- *(api)* Envtest coverage for auth, storage-ordering, ingress, nodeVersion rules - ([8c3d78e](https://github.com/toggle-corp/toggle-web-baker/commit/8c3d78e9165ba4d6aa21d8fe46926dc2f8cd1b69))
- *(e2e)* Migrate smoke sample to nodeVersion: 18 - ([4f05f58](https://github.com/toggle-corp/toggle-web-baker/commit/4f05f584b46855291163692c53e54e7bfd27c23c))
- *(e2e)* Add kind smoke pipeline, sample, AGENTS.md, CI wiring - ([005ea9d](https://github.com/toggle-corp/toggle-web-baker/commit/005ea9d25af7ce2aab3887939da75bd9d493e32f))
- *(images)* Add clone entrypoint shell test - ([1a3c06a](https://github.com/toggle-corp/toggle-web-baker/commit/1a3c06a28febca985426154a898bfd3550601891))
- Promtool alert-rule tests + e2e-local metrics assertion - ([92b8a96](https://github.com/toggle-corp/toggle-web-baker/commit/92b8a961b9ebaae52b96276d0ae472dadde79725))
- Fugit helm template snapshots - ([0aa4729](https://github.com/toggle-corp/toggle-web-baker/commit/0aa4729c81be6c8986f9b68c99b09cf7a68f32a2))

#### ⚙️ Miscellaneous Tasks

- *(chart)* Re-sync bundled CRD with new validations + ref default - ([a5c080b](https://github.com/toggle-corp/toggle-web-baker/commit/a5c080bc19d271015ebb639d1144aeecc10cd0fd))
- Gitignore .impeccable/ skill snapshots - ([09b4c53](https://github.com/toggle-corp/toggle-web-baker/commit/09b4c530ad5e884deab91829303cd21422d04333))
- Make the helm-snapshots hook a fixer (regenerate on drift) - ([9dd94e4](https://github.com/toggle-corp/toggle-web-baker/commit/9dd94e44c2c59c5dcba016831f587ac52ae8e349))
- Move helm snapshot drift check into pre-commit with pinned helm/yq - ([d3616ec](https://github.com/toggle-corp/toggle-web-baker/commit/d3616ec37f0b0ebf1512bf3f198ffe12faa00324))
- Adopt pre-commit as the single lint runner (manual + CI, no git hook) - ([5ae9ba1](https://github.com/toggle-corp/toggle-web-baker/commit/5ae9ba1a4a12a7eba3d8645eb5f9dd0fb7ff4e45))
- Content-tag node images, skip-if-published, order chart after images - ([bb4b451](https://github.com/toggle-corp/toggle-web-baker/commit/bb4b451a5725d5e06612c5bc819074f84bf9b893))
- Reusable CI + tag-triggered release workflows - ([cded65c](https://github.com/toggle-corp/toggle-web-baker/commit/cded65c69977791355f15a58514a7d5dcfbeb1ec))
- Add fugit submodule pinned at v0.2.0 - ([36199af](https://github.com/toggle-corp/toggle-web-baker/commit/36199af29bb8956058d65945b52cd2453b6d9105))

#### Build

- Automate chart CRD sync from config/crd - ([c677fea](https://github.com/toggle-corp/toggle-web-baker/commit/c677fea2baf1e5cc6da630864b494dd267c854cd))
- Operator Dockerfile + justfile task runner - ([c607a22](https://github.com/toggle-corp/toggle-web-baker/commit/c607a22d0c18b01c90ba7e59eea8d586fad51861))

#### Images

- Add platform container images for FrontendApp deploy pipeline - ([08dc807](https://github.com/toggle-corp/toggle-web-baker/commit/08dc80713cad9c12be624e25f72b54d61a08ca5e))


<!-- generated by git-cliff -->
