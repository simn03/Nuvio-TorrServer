# Family TorrServer — Stremio Addon Build Plan

A self-hosted Stremio addon, written in Go, packaged as a **single Docker container**
that supervises two processes: this addon server and a sibling **TorrServer** instance.
Prowlarr (already self-hosted by the operator) is used for indexer search. The addon
provides P2P streaming to family members over TLS with **no VPN or tunnel required** —
access control is a per-user bearer token in the URL path plus short-lived HMAC-signed
media URLs.

This document is the complete spec. Build it milestone by milestone (see the bottom).
Anything marked **VERIFY** must be confirmed against the actual library/API version at
build time rather than assumed.

---

## 1. Locked decisions (do not re-litigate)

| Area | Decision |
|------|----------|
| Language / router | Go, `github.com/go-chi/chi/v5` |
| SQLite driver | `modernc.org/sqlite` (pure Go, **CGO_ENABLED=0**, static binary) |
| Auth model | Admin-minted bearer tokens via CLI subcommand (`docker exec`). **No signup page.** |
| Token placement | URL path: `/u/{token}/...`. Validated by middleware on every `/u/*` route. |
| Config model | **Option A** — per-user config stored in DB **keyed by token**, edited at an authed `/u/{token}/configure` page. Install URL never changes; **no reinstall to change settings**. |
| Media delivery | Addon proxies TorrServer via short-lived **HMAC-signed** `/play` URLs. TorrServer is **never** exposed to the internet. |
| Scraping | **Prowlarr** search API (operator already hosts it). No Torrentio, no bespoke scrapers. |
| ID → title bridge | **Cinemeta** (`v3-cinemeta.strem.io`), unauthenticated. |
| Release-name parsing | Robust **library** (see §5), not hand-rolled regex. |
| Caching | In the **same SQLite DB** as users. Cinemeta TTL ~30d, Prowlarr results TTL ~12h. |
| Season packs | Supported — pick correct episode file inside the torrent after adding. |
| Streams returned | **Ranked list** (Torrentio-style), not a single best. |
| Ranking options | Torrentio-style, exposed as **per-user config**. |
| Multi-user isolation | All valid tokens are functionally **identical** in capability. |
| Prowlarr coupling | **Agnostic** — URL + API key via env, no assumptions about the operator's setup. |
| TorrServer preload | Preload **a bit** before returning the stream. |

---

## 2. Single-binary subcommands

One Go binary, subcommand dispatched in `cmd/addon/main.go`:

- `serve` — runs the HTTP server (what supervisord launches).
- `adduser --name "<name>"` — mints a token, seeds default config, prints the full install URL.
- `revoke --token "<token>"` — sets `active=0`.
- `listusers` — prints tokens, names, created-at, active flag.

CLI writes to the same SQLite file the server reads (`DB_PATH`), so admin ops take effect live.

---

## 3. Project layout

```
family-torrserver/
├── cmd/addon/main.go            # subcommand dispatch
├── internal/
│   ├── server/
│   │   ├── server.go            # chi router, wiring, graceful shutdown
│   │   ├── manifest.go          # GET /u/{token}/manifest.json
│   │   ├── configure.go         # GET+POST /u/{token}/configure (authed config UI)
│   │   ├── stream.go            # GET /u/{token}/stream/{type}/{id}.json
│   │   ├── play.go              # GET /play/{hash}/{idx} signed reverse-proxy
│   │   └── middleware.go        # token validation, optional rate limit
│   ├── store/
│   │   ├── store.go             # users + config CRUD
│   │   └── cache.go             # cinemeta + prowlarr result cache
│   ├── config/config.go         # UserConfig struct, defaults, (de)serialization
│   ├── torrserver/client.go     # TorrServer REST client + preload
│   ├── prowlarr/client.go       # Prowlarr search client
│   ├── cinemeta/client.go       # Cinemeta metadata client (cached)
│   ├── resolver/resolver.go     # id -> ranked []Stream (orchestrates the above)
│   ├── rank/rank.go             # parse + filter + sort per UserConfig
│   └── sign/sign.go             # HMAC sign/verify for play URLs
├── admin/admin.go               # adduser/revoke/listusers implementations
├── migrations/                  # embedded SQL, applied on startup
├── Dockerfile
├── supervisord.conf
├── docker-compose.example.yml
├── .env.example
└── go.mod
```

---

## 4. Environment / config

Read from environment (document all in `.env.example`):

- `PORT` — addon listen port (default `7000`).
- `PUBLIC_HOST` — external hostname (e.g. `torrserver.example.com`), used to build install + play URLs. Fall back to request `Host` header if unset.
- `TORRSERVER_URL` — internal, default `http://127.0.0.1:8090`.
- `PROWLARR_URL` — e.g. `http://prowlarr:9696`. **Required.**
- `PROWLARR_API_KEY` — **Required.**
- `CINEMETA_URL` — default `https://v3-cinemeta.strem.io`.
- `DB_PATH` — default `/data/addon/addon.db`.
- `SIGNING_SECRET` — HMAC secret for play URLs. **Required**; fail fast if empty or default.
- `PLAY_URL_TTL` — signed URL lifetime, default `6h`.
- `PROWLARR_CACHE_TTL` — default `12h`.
- `CINEMETA_CACHE_TTL` — default `720h` (30d).
- `TORRSERVER_PRELOAD` — bool, default `true`.
- `TORRENT_IDLE_TTL` — sweeper threshold, default `30m`.

---

## 5. Release-name parser (**VERIFY**)

Primary candidate: **`github.com/middelink/go-parse-torrent-name`** (a Go port of PTN).
It extracts `resolution`, `quality`, `codec`, `season`, `episode`, `group`, etc. from a
release title.

**VERIFY at build time** that it (a) builds cleanly under the pinned Go version and
(b) covers the season/episode and quality patterns we rely on (§7, §8). If coverage is
weak, wrap it with a small supplementary matcher for gaps (e.g. `1x05`, `S01 E05`,
absolute anime numbering) rather than replacing it. Keep the parser behind the
`rank` package interface so the library choice is swappable.

---

## 6. Data model & schema

Single SQLite DB (`modernc.org/sqlite`). Apply embedded migrations on startup.

```sql
CREATE TABLE IF NOT EXISTS users (
  token       TEXT PRIMARY KEY,        -- 32 hex chars
  name        TEXT NOT NULL,
  created_at  INTEGER NOT NULL,        -- unix ms
  active      INTEGER NOT NULL DEFAULT 1
);

-- Option A: config is a JSON blob keyed by the user's token.
CREATE TABLE IF NOT EXISTS user_config (
  token       TEXT PRIMARY KEY REFERENCES users(token) ON DELETE CASCADE,
  config_json TEXT NOT NULL,
  updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS cinemeta_cache (
  imdb_id     TEXT NOT NULL,
  kind        TEXT NOT NULL,           -- movie | series
  payload     TEXT NOT NULL,           -- JSON: {name, year, ...}
  expires_at  INTEGER NOT NULL,
  PRIMARY KEY (imdb_id, kind)
);

CREATE TABLE IF NOT EXISTS prowlarr_cache (
  query_key   TEXT PRIMARY KEY,        -- hash of normalized query+categories
  payload     TEXT NOT NULL,           -- JSON: []Result
  expires_at  INTEGER NOT NULL
);
```

Store methods:
- Users: `CreateUser(name) (token, error)`, `IsValid(token) bool`, `Revoke(token) error`, `ListUsers() []User`.
- Config: `GetConfig(token) (UserConfig, error)` (returns defaults if row missing), `SetConfig(token, UserConfig) error`.
- Cache: generic `CacheGet(table, key) ([]byte, bool)` / `CacheSet(table, key, payload, ttl)`; lazy-expire on read plus a periodic purge.

---

## 7. UserConfig (Torrentio-style options)

```go
type UserConfig struct {
    Sort            string   // "quality" | "seeders" | "size"   (default "quality")
    Resolutions     []string // subset of ["4k","1080p","720p","480p","unknown"]
    ExcludeQualities []string // e.g. ["cam","ts","scr"]  (release types to drop)
    PreferHEVC      bool     // bump x265/HEVC in ranking (default true)
    MaxSizeGB       float64  // 0 = no cap
    MinSeeders      int      // drop results below this (default e.g. 3)
    MaxResults      int      // cap streams returned (default 5)
}
```

Defaults (seeded by `adduser`): sort=quality, all resolutions enabled except none
excluded, ExcludeQualities=`["cam","ts"]`, PreferHEVC=true, MaxSizeGB=0, MinSeeders=3,
MaxResults=5. Serialize as JSON into `user_config.config_json`.

The `/configure` page (see §10) renders a form bound to these fields and POSTs back to
the same authed URL; on save, `SetConfig` updates the row. **No reinstall required** —
the manifest URL is stable, and the stream handler reads live config per request.

---

## 8. Resolver flow

Input: Stremio `type` (`movie`|`series`) and `id` (`tt1234567` or `tt1234567:S:E`).

1. **Parse ID** → imdb, season, episode (episode/season nil for movies).
2. **Cinemeta** (cached, 30d): `GET {CINEMETA_URL}/meta/{type}/{imdb}.json` → `.meta.name`, `.meta.year`.
3. **Build queries:**
   - movie: `"{name} {year}"`
   - series: primary `"{name} S{season:02d}E{episode:02d}"`, **plus** a season-pack query `"{name} S{season:02d}"` to catch packs. Merge + dedupe results.
4. **Prowlarr** (cached, 12h): `GET {PROWLARR_URL}/api/v1/search?query={q}&categories={cats}&type=search` with header `X-Api-Key`. Movies cats `2000`, TV `5000`. **VERIFY** field names on the Prowlarr version; expect per-result `title`, `size`, `seeders`, `downloadUrl`/`magnetUrl`, `infoHash`, `guid`.
5. **Parse each result title** with the §5 library → resolution, quality, codec, detected season/episode(s).
6. **Rank/filter** (`rank` package) per the requesting user's `UserConfig`:
   - drop results below `MinSeeders`, outside `Resolutions`, in `ExcludeQualities`, or above `MaxSizeGB`;
   - sort by `Sort` (with `PreferHEVC` as a tiebreak/boost);
   - truncate to `MaxResults`.
7. For each surviving result, **add to TorrServer** (§9) to obtain hash + file list.
8. **Season-pack episode selection:** if the torrent contains multiple video files
   (pack), choose the file whose parsed S/E matches the requested episode. Use the §5
   parser on each file name; fall back to episode-number-in-filename heuristics. For
   single-file results, index 0. Record the chosen `fileIndex`.
9. Build a **signed play URL** per stream (§11) and return the list.

Wrap external calls with context timeouts and `errgroup` where parallelism helps
(resolving multiple candidates concurrently). Cache aggressively at the Cinemeta and
Prowlarr layers so repeat opens are instant.

---

## 9. TorrServer client (**VERIFY** endpoints against pinned version)

TorrServer (YouROK/TorrServer, "Matrix" API). Expected shape — confirm against the
pinned release:

- **Add:** `POST /torrents` body `{"action":"add","link":"<magnet-or-hash>","save_to_db":false}` → torrent JSON incl. `hash`.
- **Get / file list:** `POST /torrents` body `{"action":"get","hash":"<hash>"}` → includes `file_stats` (id, path, length).
- **Stream:** `GET /stream/{filename}?link={hash}&index={idx}&play` (serves bytes, honors `Range`).
- **Preload:** `GET /stream/{filename}?link={hash}&index={idx}&preload` — call this when `TORRSERVER_PRELOAD=true` before returning the stream, so playback starts smoother. Keep the preload short.
- **Remove:** `POST /torrents` body `{"action":"rem","hash":"<hash>"}` — used by the sweeper.

Client interface:

```go
type Client interface {
    Add(ctx context.Context, magnet string) (hash string, files []File, err error)
    Preload(ctx context.Context, hash string, index int) error
    Remove(ctx context.Context, hash string) error
    StreamURL(hash string, index int) string // internal 127.0.0.1 URL for the proxy
}
```

**Lifecycle sweeper:** background ticker removing torrents idle beyond
`TORRENT_IDLE_TTL`, so multi-user access doesn't pile up TorrServer's cache. Track
last-access per hash in memory (updated by the `/play` handler).

---

## 10. HTTP routes

All `/u/*` routes pass through token-validation middleware (403 on missing/revoked).

- `GET /u/{token}/manifest.json` — returns manifest. `behaviorHints.configurable: true` so Stremio shows a Configure button pointing at the config page; `configurationRequired: false` (defaults work out of the box). `resources:["stream"]`, `types:["movie","series"]`, `idPrefixes:["tt"]`.
- `GET /u/{token}/configure` — HTML form bound to the user's current `UserConfig`.
- `POST /u/{token}/configure` — validate + `SetConfig`, re-render with a saved confirmation. **No new URL, no reinstall.**
- `GET /u/{token}/stream/{type}/{id}.json` — run resolver with this user's config; return `{ streams: [...] }`. Each stream: `{ url, title, name, behaviorHints:{ bingeGroup } }`. Title should be Torrentio-style and human-scannable, e.g. `1080p BluRay HEVC` / `👤 42  💾 8.2 GB`.
- `GET /play/{hash}/{idx}?exp=&sig=` — **not** under `/u/`; guarded by HMAC + expiry instead of token. Verify, then reverse-proxy to the TorrServer stream URL, passing `Range` through. Update last-access for the sweeper.

Serve manifest/stream/configure with permissive CORS as Stremio expects
(`Access-Control-Allow-Origin: *`).

---

## 11. Signed play URLs

- Payload: `"{hash}:{index}:{exp}"`, `exp` = unix ms now + `PLAY_URL_TTL`.
- `sig = hex(HMAC_SHA256(SIGNING_SECRET, payload))`.
- URL: `https://{PUBLIC_HOST}/play/{hash}/{index}?exp={exp}&sig={sig}`.
- Verify with `crypto/subtle.ConstantTimeCompare`; reject expired (`410`) or bad sig (`403`).

Rationale: even if a play URL leaks it dies within `PLAY_URL_TTL`, and TorrServer's real
endpoint is never reachable from outside the container.

---

## 12. Docker packaging

Multi-stage. Static Go binary + TorrServer release binary + supervisord.

```dockerfile
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/addon ./cmd/addon

FROM alpine:3.20
RUN apk add --no-cache supervisor curl ca-certificates
ARG TS_VERSION=v1.0.31          # VERIFY latest stable at build time
ARG TS_ARCH=amd64
RUN curl -fL -o /usr/local/bin/torrserver \
      https://github.com/YouROK/TorrServer/releases/download/${TS_VERSION}/TorrServer-linux-${TS_ARCH} \
    && chmod +x /usr/local/bin/torrserver
COPY --from=build /out/addon /app/addon
COPY supervisord.conf /etc/supervisord.conf
VOLUME ["/data/torrserver", "/data/addon"]
EXPOSE 7000
CMD ["supervisord", "-c", "/etc/supervisord.conf"]
```

```ini
# supervisord.conf
[supervisord]
nodaemon=true

[program:torrserver]
command=/usr/local/bin/torrserver --port 8090 --datadir /data/torrserver
autorestart=true
stdout_logfile=/dev/stdout
stdout_logfile_maxbytes=0
stderr_logfile=/dev/stderr
stderr_logfile_maxbytes=0

[program:addon]
command=/app/addon serve
autorestart=true
stdout_logfile=/dev/stdout
stdout_logfile_maxbytes=0
stderr_logfile=/dev/stderr
stderr_logfile_maxbytes=0
```

`docker-compose.example.yml`: join the **existing Docker network** where Prowlarr lives
(so `http://prowlarr:9696` resolves), mount `/data/addon` and `/data/torrserver`
volumes, and pass env from `.env`. Only port `7000` is published to the reverse proxy —
never `8090`.

---

## 13. Deployment (operator-facing, put in README)

Caddy reverse-proxies only the addon; TorrServer's 8090 stays internal:

```
torrserver.example.com {
    reverse_proxy family-torrserver:7000
}
```

TLS via the operator's existing Cloudflare setup. No family-facing VPN or tunnel: TLS +
path token + signed play URLs are the whole access layer. Optional hardening note:
Cloudflare WAF rule restricting the hostname to the operator's country/ASN.

Add a family member:

```bash
docker exec family-torrserver /app/addon adduser --name "Mom"
# prints: https://torrserver.example.com/u/<token>/manifest.json
```

Hand them that URL; they install in Stremio and (optionally) hit Configure to tweak
quality preferences.

---

## 14. Build milestones (execute in order)

1. **Skeleton** — module init, `main.go` subcommand dispatch, chi server, embedded
   migrations, static manifest at `/u/{token}/manifest.json`, env loading, fail-fast on
   missing `SIGNING_SECRET`/Prowlarr vars.
2. **Store + auth + CLI** — users + user_config tables, `adduser`/`revoke`/`listusers`,
   token-validation middleware. Prove the mint → install → 403-on-revoke loop with a
   stubbed stream.
3. **Config (Option A)** — `UserConfig`, defaults, `/configure` GET+POST authed page,
   live read in the stream path. Prove editing prefs changes stream output **without
   reinstall**.
4. **Cinemeta + Prowlarr + cache** — both clients, cache tables + TTLs, resolver up to
   ranked-but-not-yet-added results. Unit-test `rank` against sample titles.
5. **TorrServer + play** — client (add/preload/remove), season-pack file selection,
   signed `/play` reverse-proxy with Range passthrough, idle sweeper. Confirm seeking
   and multi-stream selection end to end.
6. **Package + deploy** — Dockerfile, supervisord, compose example, README with the
   Caddy snippet and `adduser` flow. Add a real family member and stream a season-pack
   episode start-to-finish.

Ship each milestone building and runnable before starting the next.

---

## 15. Notes for the implementer

- Prefer stdlib `net/http/httputil.ReverseProxy` for `/play` — do **not** hand-roll byte
  copying; it handles `Range`/partial content correctly, which Stremio seeking needs.
- Everything marked **VERIFY** (parser library coverage, Prowlarr result field names,
  TorrServer endpoints, latest TorrServer release tag) must be checked against the real
  versions during build; treat the shapes here as expected-but-unconfirmed.
- Keep the parser behind the `rank` interface and the three external services behind
  their own client interfaces so each is independently testable and swappable.
- All tokens are functionally identical; there is no per-user capability logic beyond
  validity + that user's ranking config.
