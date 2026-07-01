# node18

Node.js 18 base image for user frontend builds.

## Contract

| | |
|---|---|
| **Runs as** | build-pod init/main container, `USER node` (UID 1000) |
| **Base** | `node:18-alpine` pinned by digest + `bash` + `git` + corepack |
| **Selected via** | `spec.nodeVersion` on the FrontendApp (picks node 18) |
| **Adds** | `bash` (phase entrypoints are bash), `git` (repo/submodule and lockfile flows), `corepack enable` (yarn + pnpm shims) |

The official `node:18-alpine` already provides a numeric `node` user (UID 1000),
so no user is created; the image ends with `USER node` for sane local runs. The
operator overrides `runAsUser` at runtime.
