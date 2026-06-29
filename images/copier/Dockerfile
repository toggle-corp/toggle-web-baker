# copier -- THE critical, output-mutating phase image.
#
# Trusted, platform-owned. Runs as the MAIN container after every initContainer
# succeeds. The ONLY writer to the output PVC at /output. Small image: just
# rsync + coreutils + findutils on top of a digest-pinned alpine.
FROM alpine@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d

# bash for the entrypoint; rsync for --safe-links assembly; coreutils for GNU
# du -sb / df -B1 / mv -T / chown; findutils for find -printf.
RUN apk add --no-cache bash rsync coreutils findutils

COPY lib.sh /usr/local/lib/copier/lib.sh
COPY entrypoint.sh /usr/local/bin/copier-entrypoint.sh
RUN chmod 0644 /usr/local/lib/copier/lib.sh && \
    chmod 0755 /usr/local/bin/copier-entrypoint.sh

# entrypoint.sh resolves lib.sh relative to its own dir; keep them together.
RUN cp /usr/local/lib/copier/lib.sh /usr/local/bin/lib.sh

# Run as the platform user (matches clone/du and PLATFORM_OWNER=65532:65532). The
# build pod enforces runAsNonRoot, so root is not an option; running AS the
# platform user means rsync creates 65532-owned files directly and assemble()'s
# `chown -R 65532:65532` is a permitted no-op. This is exactly the ownership
# nginx's `disable_symlinks if_not_owner` requires. The output PVC dir is
# group/world-writable (local-path provisions 0777), so non-root can publish.
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/copier-entrypoint.sh"]
