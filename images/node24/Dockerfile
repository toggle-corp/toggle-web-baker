# node24 -- Node.js 24 base for user frontend builds.
#
# Trusted, platform-owned base image the operator runs for a user build when
# spec.nodeVersion selects node 24. The official node:24-alpine already ships a
# numeric `node` user (UID 1000), so no user is created here. Adds bash (the
# phase entrypoints are bash) and git (submodule/lockfile flows), and enables
# corepack so BOTH the yarn and pnpm shims resolve at runtime. Pinned by digest.
FROM node@sha256:a0b9bf06e4e6193cf7a0f58816cc935ff8c2a908f81e6f1a95432d679c54fbfd

# bash for the phase entrypoints; git for repo/submodule and lockfile flows.
# corepack enable installs the yarn/pnpm shims into /usr/local/bin (needs root).
RUN apk add --no-cache bash git && corepack enable

# The node:alpine base already provides the numeric `node` user (UID 1000); run
# as it for sane local runs. The operator overrides runAsUser at runtime anyway.
USER node

# Pre-activate a pinned pnpm AS the node user so the pinned version and its
# download land in node's corepack home (~/.cache): corepack's default version is
# per-user, so the node user resolves this pin offline instead of fetching
# "latest" at runtime. pnpm 10.x supports node 24; yarn is the bundled classic 1.x.
RUN corepack prepare pnpm@10.18.0 --activate
