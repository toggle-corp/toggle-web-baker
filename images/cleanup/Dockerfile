# cleanup -- cache-PVC pruner.
#
# Trusted, platform-owned. The operator runs this over the cache PVC (RW at
# /cache) when it exceeds a threshold. Branches on $PACKAGE_MANAGER:
#   pnpm -> `pnpm store prune` (reference-safe, content-addressed store)
#   yarn -> trim the yarn cache.
# Needs the package-manager CLIs, so it is built on a node base (corepack ships
# pnpm/yarn shims). Pinned by digest. node:22-bookworm-slim already provides
# bash and GNU coreutils (du -sb), so no extra apt layer is needed.
FROM node@sha256:813a7480f28fdadac1f7f5c824bcdad435b5bc1322a5968bbbdef8d058f9dff4

# Enable corepack so `pnpm` and `yarn` shims resolve at runtime.
RUN corepack enable

COPY entrypoint.sh /usr/local/bin/cleanup-entrypoint.sh
RUN chmod 0755 /usr/local/bin/cleanup-entrypoint.sh

# Run as the platform user (matches clone/du/copier). The operator confines phase
# pods with runAsNonRoot, so root is not an option. HOME points at /tmp because
# corepack/pnpm/yarn want a writable home and the pod uses readOnlyRootFilesystem;
# whoever wires the cleanup pod MUST mount a writable /tmp (the platform
# convention, as build/nginx pods do) and the cache PVC RW at /cache.
#
# Caveat for the pod wiring: a non-root pruner can only delete cache entries it
# can unlink, i.e. those under directories it owns. If a build phase ran as a
# DIFFERENT UID (e.g. cimg/node's 3434 via spec.pipeline.phases.build.runAsUser) and wrote into
# the shared cache, run cleanup as that same UID (or set an fsGroup) so the prune
# does not fail with EPERM on foreign-owned subtrees.
ENV HOME=/tmp
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/cleanup-entrypoint.sh"]
