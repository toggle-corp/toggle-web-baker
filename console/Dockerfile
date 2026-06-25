# Build the console (templates are embedded via go:embed, so the final image
# is a single static binary on distroless).
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/console ./cmd/console

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/console /console
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/console"]
