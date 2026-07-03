# node24 -- Node.js 24 base for user frontend builds.
#
# Trusted, platform-owned base image the operator runs for a user build when
# spec.nodeVersion selects node 24. The official node:24-alpine already ships a
# numeric `node` user (UID 1000), so no user is created here. Adds bash (the
# phase entrypoints are bash) and git (submodule/lockfile flows), and enables
# corepack so BOTH the yarn and pnpm shims work. Pinned by digest.
FROM node@sha256:a0b9bf06e4e6193cf7a0f58816cc935ff8c2a908f81e6f1a95432d679c54fbfd

# corepack's per-manager version cache. The operator runs this image with
# readOnlyRootFilesystem and injects HOME=/work (a writable emptyDir), so
# corepack's default (~/.cache under HOME) would be an EMPTY dir at runtime --
# corepack would then try to DOWNLOAD a package manager (and mkdir there),
# which fails with EROFS on the read-only root. Pinning COREPACK_HOME to a fixed,
# image-baked path decouples the cache from HOME: the versions pre-activated
# below are found offline and read-only.
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
# try to download at runtime. Pre-activate pnpm 10.x (supports node 24) + classic
# yarn into COREPACK_HOME (as root, world-readable) so both resolve offline at
# runtime regardless of the injected HOME.
RUN apk add --no-cache bash git build-base python3 \
    && corepack enable \
    && corepack prepare yarn@1.22.22 --activate \
    && corepack prepare pnpm@10.18.0 --activate

# The node:alpine base already provides the numeric `node` user (UID 1000); run
# as it for sane local runs. The operator overrides runAsUser at runtime anyway.
USER node
