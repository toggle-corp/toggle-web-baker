# du -- minimal size-probe image.
#
# Trusted, platform-owned. The operator runs this as a standalone pod to measure
# one PVC. Mounts the target read-only at /target, prints the byte count to the
# termination message, exits. Nothing else.
FROM alpine@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d

# GNU coreutils for `du -sb` (busybox du lacks -b). bash for the entrypoint.
RUN apk add --no-cache bash coreutils

COPY entrypoint.sh /usr/local/bin/du-entrypoint.sh
RUN chmod 0755 /usr/local/bin/du-entrypoint.sh

USER 65532:65532

ENTRYPOINT ["/usr/local/bin/du-entrypoint.sh"]
