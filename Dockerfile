# Images for the CMS API, the background worker and the database migrations, from one build.
#
#   docker build --target api     -t codesprout/api .
#   docker build --target worker  -t codesprout/worker .
#   docker build --target migrate -t codesprout/migrate .
#
# Every binary is static (no cgo) and runs as non-root on distroless: no shell, no package
# manager, nothing to patch but the binary itself. Health checks use the bundled
# `healthcheck` command (cmd/healthcheck) because there is no curl or wget.

# ── build ─────────────────────────────────────────────────────────────────────
FROM golang:1.27-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath

# Dependencies first: a source-only change reuses this layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# The migration tool, pinned; it is not a dependency of the service code.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go install github.com/pressly/goose/v3/cmd/goose@v3.29.0

COPY cmd cmd
COPY internal internal
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-s -w" -o /out/sprout-api ./cmd/api && \
    go build -ldflags="-s -w" -o /out/sprout-worker ./cmd/worker && \
    go build -ldflags="-s -w" -o /out/healthcheck ./cmd/healthcheck

# ── runtime base ──────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /out/healthcheck /usr/local/bin/healthcheck
USER nonroot:nonroot

# ── api ───────────────────────────────────────────────────────────────────────
FROM runtime AS api
COPY --from=build /out/sprout-api /usr/local/bin/sprout-api
ENV HTTP_ADDR=:9300
EXPOSE 9300
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=5 CMD ["healthcheck", "http://127.0.0.1:9300/readyz"]
ENTRYPOINT ["sprout-api"]

# ── worker ────────────────────────────────────────────────────────────────────
FROM runtime AS worker
COPY --from=build /out/sprout-worker /usr/local/bin/sprout-worker
# Metrics and /healthz stay on the container's loopback; the health check runs inside it.
ENV WORKER_METRICS_ADDR=127.0.0.1:9312
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=5 CMD ["healthcheck", "http://127.0.0.1:9312/healthz"]
ENTRYPOINT ["sprout-worker"]

# ── migrate ───────────────────────────────────────────────────────────────────
# One-shot: applies pending migrations (goose is idempotent) and exits. The connection string
# comes from GOOSE_DBSTRING (no shell here to expand variables).
FROM runtime AS migrate
COPY --from=build /go/bin/goose /usr/local/bin/goose
COPY db/migrations /migrations
ENV GOOSE_DRIVER=postgres GOOSE_MIGRATION_DIR=/migrations
ENTRYPOINT ["goose", "up"]
