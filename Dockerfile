# Build the manager binary
FROM golang:1.26 AS builder

WORKDIR /workspace

# Copy the Go module manifests and download dependencies.
# These layers are cached unless go.mod/go.sum change.
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# Copy the Go sources.
COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/

# Build the operator/manager binary.
RUN CGO_ENABLED=0 GOOS=linux go build -a -o manager ./cmd

# Use distroless as minimal base image to package the manager binary.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
