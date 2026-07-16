# Nuvio TorrServer — self-hosted P2P streaming server

Run your own private streaming source for **Nuvio** — the media streaming app
that speaks the Stremio addon protocol — and any other Stremio-compatible
client. One Docker container gives your household a
Torrentio-style, ranked stream list backed by your **own** TorrServer and your
**own** Prowlarr indexers — streamed to each member over your existing TLS, with
**no VPN or tunnel** and TorrServer never exposed to the internet.

<p align="center">
  <img alt="Container image" src="https://img.shields.io/badge/ghcr.io-nuvio--p2p--http--addon-2496ED?logo=docker&logoColor=white">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.23-00ADD8?logo=go&logoColor=white">
  <img alt="Single container" src="https://img.shields.io/badge/deploy-single%20container-success">
</p>

```
Nuvio / Stremio ──TLS──> reverse proxy ──> addon :7000 ──┬─> Prowlarr  (search)
                                                          ├─> Cinemeta  (id → title)
                                                          └─> TorrServer :8090 (internal only)
```

It installs like any Stremio addon: hand each member a personal
`…/u/{token}/manifest.json` install URL and they paste it into Nuvio or Stremio.
Playback is proxied through short-lived HMAC-signed URLs, so TorrServer is only
ever reachable through the addon.

## Why

- **Self-hosted & private** — your indexers, your TorrServer, your TLS. No third-party
  scraper, no public TorrServer, no VPN for viewers.
- **Single container** — the addon and a sibling [TorrServer](https://github.com/YouROK/TorrServer)
  run supervised in one image. Only port `:7000` is published.
- **Per-user, no reinstall** — each member gets a token-scoped install URL and a
  live **Configure** page (resolutions, size/seeder caps, HEVC, which indexers to
  search). Changing settings never changes the install URL.
- **Fast** — Prowlarr searches are per-query time-bounded and accumulate across
  fetches; TorrServer work is deferred to playback, so stream lists return in
  seconds instead of tens of seconds. (See [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md#performance-design).)
- **Ranked results** — Torrentio-style list with quality/size/seeder badges and the
  source indexer, season-pack episode selection included.

## Endpoints

| Route | Purpose |
|---|---|
| `GET /u/{token}/manifest.json` | Per-user install URL |
| `GET /u/{token}/configure` | Live per-user quality/ranking preferences |
| `GET /u/{token}/stream/{type}/{id}.json` | Ranked stream list |
| `GET /play/{hash}/{idx}?exp=&sig=` | HMAC-signed reverse-proxy to TorrServer, with `Range`/seeking |

## Prerequisites

- Docker + Docker Compose.
- A running **Prowlarr** with at least one indexer and its API key.
- A reverse proxy terminating TLS (Caddy, Traefik, Nginx, …).

## Quick start (prebuilt image)

The published multi-arch image (`linux/amd64`, `linux/arm64`) is the fastest path:

```bash
mkdir nuvio-torrserver && cd nuvio-torrserver
curl -fLO https://raw.githubusercontent.com/simn03/nuvio-p2p-http-addon/main/.env.example
curl -fLO https://raw.githubusercontent.com/simn03/nuvio-p2p-http-addon/main/docker-compose.example.yml
mv .env.example .env
mv docker-compose.example.yml docker-compose.yml

# 1) edit .env — set SIGNING_SECRET, PROWLARR_URL, PROWLARR_API_KEY, PUBLIC_HOST
openssl rand -base64 32   # value for SIGNING_SECRET
# 2) edit docker-compose.yml — point `image:` at the published image and set the
#    `networks:` block to the network your Prowlarr container runs on.
```

In `docker-compose.yml`, use the published image instead of `build:`:

```yaml
services:
  nuvio-torrserver:
    image: ghcr.io/simn03/nuvio-p2p-http-addon:latest
```

```bash
docker compose up -d
docker compose logs -f     # confirm both torrserver and addon start
```

## Quick start (build from source)

```bash
git clone https://github.com/simn03/nuvio-p2p-http-addon.git && cd nuvio-p2p-http-addon
cp .env.example .env       # then edit (see above)
cp docker-compose.example.yml docker-compose.yml   # then edit `networks:`
docker compose up -d --build
```

The addon fails fast on startup if `SIGNING_SECRET`, `PROWLARR_URL`, or
`PROWLARR_API_KEY` are missing.

## Reverse proxy (Caddy)

Only the addon (`:7000`) is proxied; TorrServer's `8090` stays internal.

```
torrserver.example.com {
    reverse_proxy nuvio-torrserver:7000
}
```

TLS via your existing setup (e.g. Cloudflare). The access layer is TLS + path
token + signed play URLs — no public-facing VPN or tunnel. Optional hardening:
a Cloudflare WAF rule restricting the hostname to your country/ASN.

## Adding a member

```bash
docker exec nuvio-torrserver /app/addon adduser --name "Mom"
# prints: Install URL: https://torrserver.example.com/u/<token>/manifest.json
```

Hand them that URL; they paste it into Nuvio or Stremio (Add-ons → paste URL →
Install). They can optionally hit **Configure** to tweak quality preferences —
the install URL never changes.

Other admin commands (run via `docker exec ... /app/addon <cmd>`):

| Command | Effect |
|---|---|
| `adduser --name "NAME"` | Mint a token, print the install URL |
| `listusers` | List users (token, created, active, install URL) |
| `revoke --token "TOKEN"` | Deactivate a token (takes effect live) |

## Per-user preferences (Configure page)

Each user opens **Configure** (or `/u/{token}/configure`) to set, live and
without reinstalling: sort order, resolutions, excluded release types,
prefer-HEVC, size/seeder caps, how many results to show, and **which Prowlarr
indexers to search**. Selecting a smaller, faster indexer set makes searches
quicker. Each result shows the full release title, a quality/size badge line, and
the source indexer (`🔎 <name>`).

> **No results for a title?** The list respects your filters. Niche/low-seeded
> content can be filtered out entirely by a high `minSeeders` or a low
> `maxSizeGB` — loosen those on the Configure page if a title comes back empty.

## Configuration

All process settings are environment variables — see [`.env.example`](.env.example)
for the full list and defaults. Required: `SIGNING_SECRET`, `PROWLARR_URL`,
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
(tags look like `MatriX.142.1`, **not** `vX.Y.Z`). The correct TorrServer binary
for the target CPU architecture is selected automatically at build. To use a
different release:

```bash
docker compose build --build-arg TS_VERSION=MatriX.142.1
```

## Development

Contributor setup, architecture, the request pipeline, the performance design,
testing (including the live profiler), and the release flow are documented in
**[`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md)**. TL;DR:

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
