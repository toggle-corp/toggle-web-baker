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

ENTRYPOINT ["/usr/local/bin/cleanup-entrypoint.sh"]
