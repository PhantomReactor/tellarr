# syntax=docker/dockerfile:1

# ---- Build stage ------------------------------------------------------------
FROM golang:1.25-alpine AS build

RUN apk add --no-cache gcc musl-dev git

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go install github.com/a-h/templ/cmd/templ@v0.3.1020 \
 && /go/bin/templ generate \
 && CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/tellarr ./cmd/api

# ---- Runtime stage ----------------------------------------------------------
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata aria2 su-exec \
 && addgroup -S app -g 1000 \
 && adduser -S app -G app -u 1000 \
 && mkdir -p /data/tellarr \
 && chown -R app:app /data/tellarr

WORKDIR /data/tellarr

COPY --from=build /out/tellarr /app/tellarr
COPY entrypoint.sh /app/entrypoint.sh

RUN chmod +x /app/entrypoint.sh

# Starts as root only to prepare the (host-mounted) download dir, then drops
# to the unprivileged "app" user for both the bundled aria2c and tellarr.
ENTRYPOINT ["/app/entrypoint.sh"]

ENV PORT=8234 \
    DB_URL=/data/tellarr/tellarr.db \
    DOWNLOAD_DIR=/data/downloads/tellarr \
    ARIA2_RPC_URL=http://127.0.0.1:6800/jsonrpc

VOLUME ["/data"]

EXPOSE 8234 6800

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD wget -qO- "http://127.0.0.1:${PORT}/ui/login" >/dev/null 2>&1 || exit 1
