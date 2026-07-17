# node22 -- Node.js 22 base for user frontend builds.
#
# Trusted, platform-owned base image the operator runs for a user build when
# spec.nodeVersion selects node 22. The official node:22-alpine already ships a
# numeric `node` user (UID 1000), so no user is created here. Adds bash (the
# phase entrypoints are bash) and git (submodule/lockfile flows), and enables
# corepack so BOTH the yarn and pnpm shims work. Pinned by digest.
FROM node@sha256:16e22a550f3863206a3f701448c45f7912c6896a62de43add43bb9c86130c3e2

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
# try to download at runtime. Pre-activate pnpm 10.x (supports node 22) + classic
# yarn into COREPACK_HOME (as root, world-readable) so both resolve offline at
# runtime regardless of the injected HOME.
#
# The pre-activated pnpm version must MATCH what target apps pin via their
# package.json `packageManager` field: corepack honours that pin EXACTLY and, on a
# mismatch, tries to DOWNLOAD the pinned version into COREPACK_HOME -> EROFS under
# the operator's readOnlyRootFilesystem. 10.34.5 tracks the current mapswipe-website
# pin (its e2e is node22's primary real-app validation); bump this when apps move.
RUN apk add --no-cache bash git build-base python3 \
    && corepack enable \
    && corepack prepare yarn@1.22.22 --activate \
    && corepack prepare pnpm@10.34.5 --activate

# The node:alpine base already provides the numeric `node` user (UID 1000); run
# as it for sane local runs. The operator overrides runAsUser at runtime anyway.
USER node
