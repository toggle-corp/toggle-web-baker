# clock -- minimal scheduled-tick image.
#
# Trusted, platform-owned. The operator runs this on a CronJob schedule to bump
# the rebuild "requested-at" annotation (and clear a stale manual "by") via
# kubectl. It replaces the distroless registry.k8s.io/kubectl image, which has
# NO shell and no `date` -- so it could never run the `sh -c "... $(date) ..."`
# tick command, admission failures aside. Baked USER 65532 mirrors the other
# platform images and satisfies the pod's runAsNonRoot gate.
FROM alpine@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d

# kubectl for the annotate; bash for the entrypoint. busybox `date` (already in
# alpine) provides the +%s epoch value, so no coreutils is needed.
RUN apk add --no-cache bash kubectl

COPY entrypoint.sh /usr/local/bin/clock-entrypoint.sh
RUN chmod 0755 /usr/local/bin/clock-entrypoint.sh

USER 65532:65532

ENTRYPOINT ["/usr/local/bin/clock-entrypoint.sh"]
