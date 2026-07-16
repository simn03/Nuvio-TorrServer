# syntax=docker/dockerfile:1

# ---- build the addon (static, CGO-free, cross-compiled per target arch) ----
# BUILDPLATFORM/TARGETOS/TARGETARCH are provided automatically by BuildKit so a
# single `docker buildx build --platform linux/amd64,linux/arm64` cross-compiles
# without emulation. A plain `docker build` sets them to the host platform.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/addon ./cmd/addon

# ---- runtime: addon + sibling TorrServer, supervised ----
FROM alpine:3.20
RUN apk add --no-cache supervisor curl ca-certificates tzdata

# TorrServer release. NOTE: YouROK/TorrServer tags releases as "MatriX.NNN.N"
# (verified — the old "vX.Y.Z" scheme 404s). The asset is picked from the build
# target arch (TorrServer-linux-{amd64,arm64,arm7}). Override TS_VERSION as needed.
ARG TARGETARCH
ARG TS_VERSION=MatriX.142.1
RUN set -eux; \
    case "${TARGETARCH:-amd64}" in \
      amd64) ts_arch=amd64 ;; \
      arm64) ts_arch=arm64 ;; \
      arm)   ts_arch=arm7  ;; \
      *)     ts_arch="${TARGETARCH}" ;; \
    esac; \
    curl -fL -o /usr/local/bin/torrserver \
      "https://github.com/YouROK/TorrServer/releases/download/${TS_VERSION}/TorrServer-linux-${ts_arch}"; \
    chmod +x /usr/local/bin/torrserver

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
