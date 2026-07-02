# shim -- phase wrapper that measures TRUE per-phase peak memory.
#
# Trusted, platform-owned. A single static binary on scratch: the shim-install
# init container runs `/shim install /baker/shim` to place it on a shared
# emptyDir, and every user phase (setup/fetch/build) is wrapped as
# `/baker/shim -- <user command>` so the phase's cgroup memory.peak lands in
# the container termination message. No shell, no libc, no attack surface.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/shim .

FROM scratch
COPY --from=build /out/shim /shim
USER 65532:65532
ENTRYPOINT ["/shim"]
