# clone -- platform-locked, digest-pinned source checkout image.
#
# Trusted, platform-owned. The operator runs this as the FIRST initContainer.
# It performs an anonymous (or optionally credentialed) git clone of a PUBLIC
# repository into the shared work volume at /workspace/src.
#
# Pinned by digest so the image the operator runs is byte-for-byte the one we
# audited. Bump deliberately via images/Makefile (CLONE_BASE_DIGEST).
FROM alpine/git@sha256:c0280cf9572316299b08544065d3bf35db65043d5e3963982ec50647d2746e26

# alpine/git's own ENTRYPOINT is ["git"]; override it with our wrapper.
COPY entrypoint.sh /usr/local/bin/clone-entrypoint.sh
COPY git-askpass.sh /usr/local/bin/git-askpass.sh
RUN chmod 0755 /usr/local/bin/clone-entrypoint.sh /usr/local/bin/git-askpass.sh

# Run as a non-root, unprivileged uid. The operator may override; nothing here
# requires root. /workspace is provided by the shared work volume at runtime.
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/clone-entrypoint.sh"]
