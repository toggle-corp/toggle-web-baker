# Changelog

## [0.1.0-dev7] - 2026-06-30
### Changes:

#### 🚀  Features

- *(api)* Add BuildStep timeline, build history, podName & Aborted to status - ([ea57299](https://github.com/toggle-corp/toggle-web-baker/commit/ea57299f579b67e02f1aad460beda62b9588eb35))
- *(console)* Flow strip, build history, storage bars & log pane - ([f6c62b4](https://github.com/toggle-corp/toggle-web-baker/commit/f6c62b45868fec93a5cb8f011793eae4012927ad))
- *(console)* View-model mapping, humanizers & Loki log client - ([a200ecf](https://github.com/toggle-corp/toggle-web-baker/commit/a200ecfd02093fe5f817f7043c1a80b751b3f788))
- *(console)* Follow system theme with System/Light/Dark toggle - ([01b0f64](https://github.com/toggle-corp/toggle-web-baker/commit/01b0f64aa00f3f8d20cf92446fe7d3587af76d4b))
- *(console)* Add logout link and themed signed-out page - ([8172a08](https://github.com/toggle-corp/toggle-web-baker/commit/8172a080d701c9643127645d8f74731faed23393))
- *(console)* Expose /healthz for external uptime monitoring - ([7803a6c](https://github.com/toggle-corp/toggle-web-baker/commit/7803a6c77b24d999a65f3264b41c311952b844dc))
- *(domain)* Build-trigger predicate (single active build, sole creator) - ([92a9fae](https://github.com/toggle-corp/toggle-web-baker/commit/92a9fae0819ec643069ce7b228358136477031ba))
- *(domain)* Build-relevant-spec hash + staleness detection - ([4f421ef](https://github.com/toggle-corp/toggle-web-baker/commit/4f421efc1ba7c7bab3bc50f23359ed18872e3964))
- *(domain)* Registry allowlist check (reconcile-time, fail-closed) - ([4d8125f](https://github.com/toggle-corp/toggle-web-baker/commit/4d8125f6b3014767a9bfbb89e6a2979cfebaf065))
- *(domain)* Storage threshold ordering validation - ([064818b](https://github.com/toggle-corp/toggle-web-baker/commit/064818be60735d1cc096a6206b1d706afc0da9b8))
- *(operator)* Measure cache+dataCache PVCs via du Jobs, compute thresholdState - ([f2bce52](https://github.com/toggle-corp/toggle-web-baker/commit/f2bce52a3b00dad596e91da6e80e1b5eaea02328))
- *(operator)* Per-step timeline, build history, trigger & pod-watch - ([d69ba98](https://github.com/toggle-corp/toggle-web-baker/commit/d69ba98c94f9ce8baea210b8d6c3b7a22beb3f39))
- *(operator)* Make spec.submodules actually control recursion - ([d9e27b5](https://github.com/toggle-corp/toggle-web-baker/commit/d9e27b5ba3441e3e90dd92796ca7bceaa660aa59))
- *(operator)* Validate FrontendApp at apply time (CEL) + envtest - ([11b05dc](https://github.com/toggle-corp/toggle-web-baker/commit/11b05dc3c240ba1f5f634b76cb2c920bd98e43de))
- Release.sh wrapper (fugit) + CHANGELOG seed - ([88f89e9](https://github.com/toggle-corp/toggle-web-baker/commit/88f89e933e1e8517b471b59ac8be20f8bca46da2))
- Helm chart (operator + optional console) - ([daa1ade](https://github.com/toggle-corp/toggle-web-baker/commit/daa1adef957356f95d65511d7fb48794d2e0e6b9))
- FrontendApp Kubernetes operator - ([0ce5537](https://github.com/toggle-corp/toggle-web-baker/commit/0ce5537931eae356c20bb12425ee99bcc574791d))

#### 🐛 Bug Fixes

- *(ci)* Green the pipeline (docker push bool, lint action v7, snapshots) - ([1544de7](https://github.com/toggle-corp/toggle-web-baker/commit/1544de721304918995da5ddbff3da47236fb6e1d))
- *(clone)* Fetch top-level submodules only, not recursively - ([0cbdf69](https://github.com/toggle-corp/toggle-web-baker/commit/0cbdf691df3d5a10b2996a962cf9432d6b569c61))
- *(console)* Redirect unauthenticated users to GitHub via oauth2-proxy upstream mode - ([a482a43](https://github.com/toggle-corp/toggle-web-baker/commit/a482a43242a4a03ed26a99baeb99a9a7d0c9c600))
- *(copier)* Emit release.current + sizes map to match operator contract - ([99879fe](https://github.com/toggle-corp/toggle-web-baker/commit/99879fe1cfe0b10c06de98585dca45455cdc4b96))
- *(copier)* Allow leading-dash filenames in the source tree - ([bb06c03](https://github.com/toggle-corp/toggle-web-baker/commit/bb06c033198f0279d47f66b9df9e72c1b0dafdbc))
- *(domain)* Close review findings (transitivity, nil/empty hash, allowlist boundary) - ([d023d58](https://github.com/toggle-corp/toggle-web-baker/commit/d023d586a337b435b741c7db7d222c156567089d))
- *(e2e)* Make kind smoke pass end-to-end; non-root build pipeline - ([5257d92](https://github.com/toggle-corp/toggle-web-baker/commit/5257d92f6f1c4f5ce32cdb5724cbb21eb8b54a51))
- *(e2e)* Fail fast on failed build; review cleanups - ([b365a6f](https://github.com/toggle-corp/toggle-web-baker/commit/b365a6fb11c5e5e0ea28cf7414ab2e4592ec1c14))
- *(operator)* Mark release step done on copier success - ([246b6bb](https://github.com/toggle-corp/toggle-web-baker/commit/246b6bbf37c2a8b9b34202da532813652635f98f))
- *(operator)* Drive clone/copier via env, no-op optional phases - ([8945e31](https://github.com/toggle-corp/toggle-web-baker/commit/8945e31c658bd57f83678a1b4d94b57d468b6afc))
- *(operator)* Stop wiping immutable PVC/Service spec on reconcile - ([c26f1bb](https://github.com/toggle-corp/toggle-web-baker/commit/c26f1bbc393730987502da950d8881fafd1565fe))
- *(operator)* Resolve 10 code-review findings in FrontendApp operator - ([02ace71](https://github.com/toggle-corp/toggle-web-baker/commit/02ace717138a130af59b65a0fb95db0a0349209e))
- *(rbac)* Grant operator leader-election lease access - ([7111a99](https://github.com/toggle-corp/toggle-web-baker/commit/7111a99296c6cbe8829793bdb41feb3d67c265ee))
- *(review)* Harden log container picker, step fallback & pod watch - ([1fc0df9](https://github.com/toggle-corp/toggle-web-baker/commit/1fc0df9aa7fe13a7b00bfd910408a6de41e77246))
- Resolve code-review findings (release-blocking) - ([7c689b3](https://github.com/toggle-corp/toggle-web-baker/commit/7c689b37c936136f0ac09fdd593f468e396be5bb))

#### 🚜 Refactor

- *(console)* Address code-review findings on the oauth2 rewiring - ([f10a7fd](https://github.com/toggle-corp/toggle-web-baker/commit/f10a7fd4b92bb45f549ad798915adb6620a6cd59))
- Flat toggle-web-baker-<image> registry scheme - ([dbaf872](https://github.com/toggle-corp/toggle-web-baker/commit/dbaf8729beeebb00798dc5646865aed3833cfd06))

#### 📚 Documentation

- *(agents)* Require helm snapshot check before every commit - ([7d02a0a](https://github.com/toggle-corp/toggle-web-baker/commit/7d02a0a8bc28fbea7f0f75e7ba94ad3ff4227db8))
- ClusterCIDRs placeholder note + helm upgrade --install - ([2549e67](https://github.com/toggle-corp/toggle-web-baker/commit/2549e6717cfdda199471ab6c806529c78b05d20e))
- Use toggle-baker-system as the install namespace - ([5a4bf25](https://github.com/toggle-corp/toggle-web-baker/commit/5a4bf252d5ac2fc61a8f8851f41ca7ef408af4fd))
- Note chart ships helper images by tag, not digest - ([680d334](https://github.com/toggle-corp/toggle-web-baker/commit/680d334d3aa90b2e05d702b59d9ecf73c529cd2c))
- Operator security invariants (updated with grilling resolutions) - ([cf8a6d7](https://github.com/toggle-corp/toggle-web-baker/commit/cf8a6d75788fd1bbb428f09af631330add23425e))

#### 🧪 Testing

- *(e2e)* Add kind smoke pipeline, sample, AGENTS.md, CI wiring - ([005ea9d](https://github.com/toggle-corp/toggle-web-baker/commit/005ea9d25af7ce2aab3887939da75bd9d493e32f))
- *(images)* Add clone entrypoint shell test - ([1a3c06a](https://github.com/toggle-corp/toggle-web-baker/commit/1a3c06a28febca985426154a898bfd3550601891))
- Fugit helm template snapshots - ([0aa4729](https://github.com/toggle-corp/toggle-web-baker/commit/0aa4729c81be6c8986f9b68c99b09cf7a68f32a2))

#### ⚙️ Miscellaneous Tasks

- *(chart)* Re-sync bundled CRD with new validations + ref default - ([a5c080b](https://github.com/toggle-corp/toggle-web-baker/commit/a5c080bc19d271015ebb639d1144aeecc10cd0fd))
- Reusable CI + tag-triggered release workflows - ([cded65c](https://github.com/toggle-corp/toggle-web-baker/commit/cded65c69977791355f15a58514a7d5dcfbeb1ec))
- Add fugit submodule pinned at v0.2.0 - ([36199af](https://github.com/toggle-corp/toggle-web-baker/commit/36199af29bb8956058d65945b52cd2453b6d9105))

#### Build

- Automate chart CRD sync from config/crd - ([c677fea](https://github.com/toggle-corp/toggle-web-baker/commit/c677fea2baf1e5cc6da630864b494dd267c854cd))
- Operator Dockerfile + justfile task runner - ([c607a22](https://github.com/toggle-corp/toggle-web-baker/commit/c607a22d0c18b01c90ba7e59eea8d586fad51861))

#### Images

- Add platform container images for FrontendApp deploy pipeline - ([08dc807](https://github.com/toggle-corp/toggle-web-baker/commit/08dc80713cad9c12be624e25f72b54d61a08ca5e))


<!-- generated by git-cliff -->
