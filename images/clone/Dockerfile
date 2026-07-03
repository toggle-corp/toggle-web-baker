# clone -- platform-locked, digest-pinned source checkout image.
#
# Trusted, platform-owned. The operator runs this as the FIRST initContainer.
# It performs an anonymous (or optionally credentialed) git clone of a PUBLIC
# repository into the shared work volume at /workspace/src.
#
# Pinned by digest so the image the operator runs is byte-for-byte the one we
# audited. Bump deliberately via images/Makefile (CLONE_BASE_DIGEST).
FROM alpine/git@sha256:c0280cf9572316299b08544065d3bf35db65043d5e3963982ec50647d2746e26

# bash for the entrypoint (alpine/git ships only busybox ash; our entrypoints
# use #!/usr/bin/env bash + set -o pipefail). Added before the COPYs so editing
# a script doesn't bust the apk layer cache.
RUN apk add --no-cache bash

# alpine/git's own ENTRYPOINT is ["git"]; override it with our wrapper.
COPY entrypoint.sh /usr/local/bin/clone-entrypoint.sh
COPY git-askpass.sh /usr/local/bin/git-askpass.sh
RUN chmod 0755 /usr/local/bin/clone-entrypoint.sh /usr/local/bin/git-askpass.sh

# Run as a non-root, unprivileged uid. This is only a sane default for standalone
# runs: the operator OVERRIDES runAsUser at runtime to the build phase's UID, so
# the checkout this container writes is owned by — and writable by — the build
# phases (in-tree codegen). Nothing here requires root. /workspace is provided by
# the shared work volume at runtime.
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/clone-entrypoint.sh"]
