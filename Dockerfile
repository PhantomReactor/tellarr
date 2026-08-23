# syntax=docker/dockerfile:1

# ---- Build stage ------------------------------------------------------------
# CGO is required by mattn/go-sqlite3, so we compile with gcc on alpine/musl.
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
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S app -g 1000 \
 && adduser -S app -G app -u 1000 \
 && mkdir -p /data && chown -R app:app /data

WORKDIR /app
COPY --from=build /out/tellarr /app/tellarr

USER app

ENV PORT=8080 \
    DB_URL=/data/tellarr.db \
    DOWNLOAD_DIR=/data/downloads

VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD wget -qO- "http://127.0.0.1:${PORT}/ui/login" >/dev/null 2>&1 || exit 1

CMD ["/app/tellarr"]
