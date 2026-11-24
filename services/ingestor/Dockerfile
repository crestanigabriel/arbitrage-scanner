# syntax=docker/dockerfile:1
# Multi-stage build of the ingestor: a static Go binary on a distroless base, running as
# non-root. Build context: services/ingestor (generated code lives in internal/gen).

FROM golang:1.27-trixie AS build
WORKDIR /src
# Dependencies first, so source changes reuse this layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ingestor ./cmd/ingestor

# static: no libc needed with CGO_ENABLED=0. nonroot: uid 65532.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/ingestor /ingestor
# /metrics and /healthz (HTTP_ADDR).
EXPOSE 8080
# Distroless has no shell, curl or wget: the binary checks its own /healthz.
HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=3 CMD ["/ingestor", "healthcheck"]
ENTRYPOINT ["/ingestor"]
