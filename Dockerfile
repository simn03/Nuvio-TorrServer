# syntax=docker/dockerfile:1

# ---- build the addon (static, CGO-free, cross-compiled per target arch) ----
# BUILDPLATFORM/TARGETOS/TARGETARCH are provided automatically by BuildKit so a
# single `docker buildx build --platform linux/amd64,linux/arm64` cross-compiles
# without emulation. A plain `docker build` sets them to the host platform.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
# Addon version reported in the Stremio manifest; the release workflow passes
# the git tag (sans "v").
ARG VERSION=0.2.0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath \
      -ldflags="-s -w -X github.com/simn03/nuvio-p2p-http-addon/internal/server.addonVersion=${VERSION}" \
      -o /out/addon ./cmd/addon

# ---- runtime: addon + sibling TorrServer, supervised ----
FROM alpine:3.24
RUN apk add --no-cache supervisor curl ca-certificates tzdata

# TorrServer release. NOTE: YouROK/TorrServer tags releases as "MatriX.NNN[.N]"
# and deletes superseded point releases (".N"), so pin a base tag like
# "MatriX.147" — those persist. The asset is picked from the build target arch
# and verified against its SHA-256. To bump: change TS_VERSION and both
# checksums (`gh release view <tag> -R YouROK/TorrServer --json assets`).
ARG TARGETARCH
ARG TS_VERSION=MatriX.147
ARG TS_SHA256_AMD64=2f20fcb1fcacc9b61b7ca4bb6d5b7933547bd7f447b5cc77a84406faa9e6a4e8
ARG TS_SHA256_ARM64=fbe0f668a2a22801db891afb52e1b96e4ec762dcbf979bec2a45917848456a1c
RUN set -eux; \
    case "${TARGETARCH:-amd64}" in \
      amd64) ts_arch=amd64; ts_sha="${TS_SHA256_AMD64}" ;; \
      arm64) ts_arch=arm64; ts_sha="${TS_SHA256_ARM64}" ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fL -o /usr/local/bin/torrserver \
      "https://github.com/YouROK/TorrServer/releases/download/${TS_VERSION}/TorrServer-linux-${ts_arch}"; \
    echo "${ts_sha}  /usr/local/bin/torrserver" | sha256sum -c -; \
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
