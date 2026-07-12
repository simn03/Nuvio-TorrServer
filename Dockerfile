# syntax=docker/dockerfile:1

# ---- build the addon (static, CGO-free) ----
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/addon ./cmd/addon

# ---- runtime: addon + sibling TorrServer, supervised ----
FROM alpine:3.20
RUN apk add --no-cache supervisor curl ca-certificates tzdata

# TorrServer release. NOTE: YouROK/TorrServer tags releases as "MatriX.NNN.N"
# (verified — the old "vX.Y.Z" scheme 404s). The linux amd64 asset is
# "TorrServer-linux-amd64". Override TS_VERSION/TS_ARCH at build time as needed.
ARG TS_VERSION=MatriX.142.1
ARG TS_ARCH=amd64
RUN curl -fL -o /usr/local/bin/torrserver \
      "https://github.com/YouROK/TorrServer/releases/download/${TS_VERSION}/TorrServer-linux-${TS_ARCH}" \
    && chmod +x /usr/local/bin/torrserver

COPY --from=build /out/addon /app/addon
COPY supervisord.conf /etc/supervisord.conf
COPY docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh

# Persist both the addon DB and TorrServer's cache/config across restarts.
VOLUME ["/data/torrserver", "/data/addon"]

# Only the addon port is exposed; TorrServer's 8090 stays internal.
EXPOSE 7000

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD curl -fsS http://127.0.0.1:7000/healthz || exit 1

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["supervisord", "-c", "/etc/supervisord.conf"]
