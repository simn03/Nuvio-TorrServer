# Family TorrServer — Stremio addon

A self-hosted Stremio addon (Go) packaged as a **single Docker container** that
supervises two processes: the addon server and a sibling
[TorrServer](https://github.com/YouROK/TorrServer). It provides P2P streaming to
family members over your existing TLS — **no VPN or tunnel required**. Access
control is a per-user bearer token in the URL path plus short-lived HMAC-signed
media URLs; TorrServer is never exposed to the internet.

Indexer search uses your existing **Prowlarr**. Metadata comes from **Cinemeta**.

## How it works

```
Stremio ──TLS──> reverse proxy ──> addon :7000 ──┬─> Prowlarr  (search)
                                                  ├─> Cinemeta  (id → title)
                                                  └─> TorrServer :8090 (internal only)
```

- `GET /u/{token}/manifest.json` — install URL, unique per user.
- `GET /u/{token}/configure` — per-user quality/ranking preferences (no reinstall to change).
- `GET /u/{token}/stream/{type}/{id}.json` — ranked, Torrentio-style stream list.
- `GET /play/{hash}/{idx}?exp=&sig=` — HMAC-signed reverse-proxy to TorrServer, with `Range`/seeking.

## Prerequisites

- Docker + Docker Compose.
- A running **Prowlarr** with at least one indexer and its API key.
- A reverse proxy terminating TLS (Caddy, Traefik, Nginx, …).

## Quick start

```bash
git clone <this-repo> && cd family-torrserver
cp .env.example .env
# edit .env — set SIGNING_SECRET, PROWLARR_URL, PROWLARR_API_KEY, PUBLIC_HOST
openssl rand -base64 32   # use this for SIGNING_SECRET

cp docker-compose.example.yml docker-compose.yml
# edit docker-compose.yml — set the `networks:` block to the network your
# Prowlarr container already runs on, so http://prowlarr:9696 resolves.

docker compose up -d --build
docker compose logs -f     # confirm both torrserver and addon start
```

The addon fails fast on startup if `SIGNING_SECRET`, `PROWLARR_URL`, or
`PROWLARR_API_KEY` are missing.

## Reverse proxy (Caddy)

Only the addon (`:7000`) is proxied; TorrServer's `8090` stays internal.

```
torrserver.example.com {
    reverse_proxy family-torrserver:7000
}
```

TLS via your existing setup (e.g. Cloudflare). The access layer is TLS + path
token + signed play URLs — no family-facing VPN or tunnel. Optional hardening:
a Cloudflare WAF rule restricting the hostname to your country/ASN.

## Adding a family member

```bash
docker exec family-torrserver /app/addon adduser --name "Mom"
# prints: Install URL: https://torrserver.example.com/u/<token>/manifest.json
```

Hand them that URL; they paste it into Stremio (Add-ons → paste URL → Install).
They can optionally hit **Configure** to tweak quality preferences — the install
URL never changes.

Other admin commands (run via `docker exec ... /app/addon <cmd>`):

| Command | Effect |
|---|---|
| `adduser --name "NAME"` | Mint a token, print the install URL |
| `listusers` | List users (token, created, active, install URL) |
| `revoke --token "TOKEN"` | Deactivate a token (takes effect live) |

## Configuration

All settings are environment variables — see [`.env.example`](.env.example) for
the full list and defaults. Required: `SIGNING_SECRET`, `PROWLARR_URL`,
`PROWLARR_API_KEY`.

### Reaching Prowlarr (and the private-CA gotcha)

**Recommended:** join Prowlarr's Docker network and set
`PROWLARR_URL=http://prowlarr:9696`. Internal HTTP sidesteps all TLS trust
issues — and, importantly, the `downloadUrl` links Prowlarr returns will also be
`http://`, so TorrServer can fetch them directly.

If instead you point `PROWLARR_URL` at an **HTTPS** endpoint served by a private
/ internal CA (e.g. Caddy's `tls internal`), the container won't trust it by
default. You then have two options:

1. **Mount the CA (makes both processes trust Prowlarr):** put your CA cert(s)
   in a `./certs/` dir and mount it at `/certs` (see the commented volume in the
   compose file). The entrypoint installs them at startup. This is required if
   you want `downloadUrl`-only results to work over HTTPS, because *TorrServer*
   also fetches from Prowlarr.
2. **`PROWLARR_INSECURE_TLS=true`:** skips verification for the **addon's**
   Prowlarr calls only. Search works, but TorrServer still can't fetch HTTPS
   `downloadUrl` links from a private-CA Prowlarr — so results that offer only a
   `downloadUrl` (no magnet/infohash) are skipped. Magnet-based results work.

## Data & persistence

Two named volumes:

- `/data/addon` — SQLite DB (users, per-user config, caches).
- `/data/torrserver` — TorrServer cache/config.

Back up `/data/addon` to preserve users and their settings.

## TorrServer version

The image pins a verified TorrServer release via the `TS_VERSION` build arg
(tags look like `MatriX.142.1`, **not** `vX.Y.Z`). To use a different release:

```bash
docker compose build --build-arg TS_VERSION=MatriX.142.1
```

## Development

```bash
go test ./...
go build -o addon ./cmd/addon
SIGNING_SECRET=dev PROWLARR_URL=... PROWLARR_API_KEY=... ./addon serve
```

## Security notes

- TorrServer's port is never published; only the addon's `:7000` is.
- Play URLs are HMAC-signed and expire (`PLAY_URL_TTL`, default 6h), so a leaked
  URL dies quickly and can't reach TorrServer directly.
- All tokens are functionally identical in capability; there is no signup page —
  the admin mints tokens via the CLI.
