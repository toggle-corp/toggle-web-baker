# node18

Node.js 18 base image for user frontend builds.

## Contract

| | |
|---|---|
| **Runs as** | build-pod init/main container, `USER node` (UID 1000) |
| **Base** | `node:18-alpine` pinned by digest |
| **Selected via** | `spec.pipeline.nodeVersion` on the FrontendApp (picks node 18) |
| **Adds** | `bash` (phase entrypoints are bash), `git` (repo/submodule and lockfile flows), `build-base` + `python3` (node-gyp native addons), `corepack` with **yarn 1.22.22 + pnpm 9.15.9** pre-activated |

The official `node:18-alpine` already provides a numeric `node` user (UID 1000),
so no user is created; the image ends with `USER node` for sane local runs. The
operator overrides `runAsUser` at runtime.

`COREPACK_HOME` is baked to a fixed path with yarn+pnpm pre-activated, so both
package managers resolve **offline** under the operator's `readOnlyRootFilesystem`
+ injected `HOME=/work` — no runtime download (which would EROFS, or fetch a
node-incompatible "latest" pnpm that crashes on node 18).
