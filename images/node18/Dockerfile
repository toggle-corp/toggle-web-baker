# node18 -- Node.js 18 base for user frontend builds.
#
# Trusted, platform-owned base image the operator runs for a user build when
# spec.nodeVersion selects node 18. The official node:18-alpine already ships a
# numeric `node` user (UID 1000), so no user is created here. Adds bash (the
# phase entrypoints are bash) and git (submodule/lockfile flows), and enables
# corepack so BOTH the yarn and pnpm shims resolve at runtime. Pinned by digest.
FROM node@sha256:8d6421d663b4c28fd3ebc498332f249011d118945588d0a35cb9bc4b8ca09d9e

# bash for the phase entrypoints; git for repo/submodule and lockfile flows.
# corepack enable installs the yarn/pnpm shims into /usr/local/bin (needs root).
RUN apk add --no-cache bash git && corepack enable

# The node:alpine base already provides the numeric `node` user (UID 1000); run
# as it for sane local runs. The operator overrides runAsUser at runtime anyway.
USER node

# Pre-activate a pnpm pinned to a line that supports node 18, AS the node user so
# the pinned version and its download land in node's corepack home (~/.cache) --
# corepack's default version is per-user, so activating as root would leave the
# node user fetching "latest" pnpm at runtime, and current pnpm (11.x) needs node
# 20+ and crashes on node 18. yarn ships as the bundled classic 1.x shim.
RUN corepack prepare pnpm@9.15.9 --activate
