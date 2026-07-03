# node18 -- Node.js 18 base for user frontend builds.
#
# Trusted, platform-owned base image the operator runs for a user build when
# spec.nodeVersion selects node 18. The official node:18-alpine already ships a
# numeric `node` user (UID 1000), so no user is created here. Adds bash (the
# phase entrypoints are bash) and git (submodule/lockfile flows), and enables
# corepack so BOTH the yarn and pnpm shims work. Pinned by digest.
FROM node@sha256:8d6421d663b4c28fd3ebc498332f249011d118945588d0a35cb9bc4b8ca09d9e

# corepack's per-manager version cache. The operator runs this image with
# readOnlyRootFilesystem and injects HOME=/work (a writable emptyDir), so
# corepack's default (~/.cache under HOME) would be an EMPTY dir at runtime --
# corepack would then try to DOWNLOAD a package manager (and mkdir there),
# fetching "latest" (pnpm 11.x needs node 20+ and CRASHES on node 18) or hitting
# EROFS. Pinning COREPACK_HOME to a fixed, image-baked path decouples the cache
# from HOME: the versions pre-activated below are found offline and read-only.
ENV COREPACK_HOME=/opt/corepack
# corepack's default, when a project has no packageManager field, is to WRITE one
# into package.json. The operator now runs clone under the build phase's UID, so
# the checkout is WRITABLE by this toolchain (enabling in-tree codegen) — meaning
# that auto-pin would silently SUCCEED, mutating the user's committed manifest
# with a non-reproducible packageManager pin. Disable it: the toolchain may write
# BUILD ARTEFACTS into the tree, but must not rewrite the user's source manifest.
ENV COREPACK_ENABLE_AUTO_PIN=0

# bash for the phase entrypoints; git for repo/submodule and lockfile flows;
# build-base + python3 for node-gyp (native addons some deps compile on install --
# this is the "anything else from cimg/node" part: cimg bundles build tools).
# corepack enable installs the yarn/pnpm shims into /usr/local/bin, REPLACING the
# bundled yarn binary with a shim -- so yarn too must be pre-activated or it will
# try to download at runtime. Pre-activate node18-compatible pnpm + classic yarn
# into COREPACK_HOME (as root, world-readable) so both resolve offline at runtime
# regardless of the injected HOME.
RUN apk add --no-cache bash git build-base python3 \
    && corepack enable \
    && corepack prepare yarn@1.22.22 --activate \
    && corepack prepare pnpm@9.15.9 --activate

# The node:alpine base already provides the numeric `node` user (UID 1000); run
# as it for sane local runs. The operator overrides runAsUser at runtime anyway.
USER node
