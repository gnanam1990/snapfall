# Snapfall daemon: the owner API the dashboard talks to (approvals, workforce, event stream).
#
# Build context is daemon/ (the repo-root .dockerignore excludes daemon/):
#   docker build -t snapfall-daemon daemon
#
# The image carries no keys and no chain deployment: it starts the same keyless daemon as a
# local `go run ./cmd/snapfall`, so chain flows stop at *.pending_chain. State lives under
# /var/data, which must be a persistent disk; without one every restart starts empty.
#
# The owner API binds all interfaces, which the daemon refuses unless SNAPFALL_OWNER_TOKEN
# (>= 32 bytes) is set and SNAPFALL_OWNER_ALLOWED_HOSTS names the host clients connect to.
# Run it on a private network only (for example a Render private service), never public.

# ---- build ----
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# modernc.org/sqlite is pure Go, so the binary is static.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/snapfall ./cmd/snapfall

# ---- run ----
FROM alpine:3.22
RUN addgroup -S snapfall && adduser -S -G snapfall snapfall \
 && mkdir -p /var/data && chown snapfall:snapfall /var/data
WORKDIR /app
COPY --from=build /out/snapfall /app/snapfall
COPY manifests /app/manifests

ENV SNAPFALL_DB_PATH=/var/data/snapfall.db \
    SNAPFALL_MEMORY_DIR=/var/data/memory/jobs \
    SNAPFALL_MANIFEST_DIR=/app/manifests

USER snapfall
EXPOSE 4010
CMD ["/app/snapfall", "-api-addr", "0.0.0.0:4010"]
