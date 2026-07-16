# Developer guide

This is the contributor-facing companion to the [README](../README.md). It covers
the architecture, the request pipeline, the performance design, local
development, testing (including the live profiler), and the release flow.

## Prerequisites

- **Go 1.23+** (the module targets `go 1.23`).
- **Docker + Buildx** for image builds.
- A reachable **Prowlarr** and **TorrServer** only for the integration/profiling
  paths — the unit tests are fully offline.

The SQLite driver is `modernc.org/sqlite` (pure Go), so everything builds with
`CGO_ENABLED=0` into a static binary. Keep it that way.

## Repository layout

```
cmd/addon/            entrypoint + CLI subcommands (serve, adduser, listusers, revoke, logwrap)
internal/
  server/             chi router + HTTP handlers (stream, play, configure, manifest, middleware)
  resolver/           the pipeline: id -> cinemeta -> prowlarr search -> rank -> stream list
  rank/               release-name parsing + filtering/sorting (Torrentio-style)
  prowlarr/           Prowlarr search client + accumulating result cache
  cinemeta/           Cinemeta metadata client (id -> title/year)
  torrserver/         TorrServer "Matrix" API client + idle sweeper
  store/              single SQLite DB: users, per-user config, and all caches
  config/             per-user UserConfig (JSON blob keyed by token)
  settings/           process configuration from environment variables
  sign/               short-lived HMAC signing for /play URLs
  logging/            slog setup (text|json, level)
migrations/           embedded *.sql, applied lexicographically on startup
```

## Request pipeline

A stream request flows:

```
handleStream (server/stream.go)
  └─ Resolver.Resolve (resolver/resolver.go)
       ├─ Candidates: Cinemeta.Get → buildQueries → searchAll (Prowlarr) → filter → rank.Rank
       └─ enrich (per candidate, no network): register add-link, plan file index
  → buildPlayURL (signed) per stream
```

Playback flows:

```
handlePlay (server/play.go)
  ├─ signer.Verify(hash, index, season, episode, exp, sig)
  ├─ torr.EnsureAdded(hash) → real infohash (adds via registered link on demand)
  ├─ if index == AutoSelectFile: resolver.PlayFileIndex(...) picks the episode's file (memoized)
  └─ reverse-proxy to TorrServer StreamURL, passing Range through for seeking
```

The addon logs every stage with durations and result counts (see
`resolve: candidates ready` / `resolve: complete`), tagged with the chi request
id — this is the primary tool for understanding where time goes.

## Performance design

Three deliberate choices keep stream lists fast:

1. **Bounded search.** `resolver.searchAll` gives each Prowlarr query its own
   deadline (`PROWLARR_SEARCH_TIMEOUT`, default 12s). A per-query failure is
   non-fatal — the list still returns from the queries that succeeded — so one
   slow indexer can't hold the whole response.

2. **Accumulating Prowlarr cache.** `prowlarr.Search` merges the previously
   cached results with a fresh live search into a deduped union, re-stores the
   union with a refreshed TTL, and falls back to the cached union if the live
   search fails or is cut short. Repeated fetches of the same media therefore
   grab progressively more results as different indexers respond on different
   runs.

3. **Deferred TorrServer work.** `enrich` makes **no network calls**. Every
   candidate registers its add-link locally — under its real infohash, or under
   a synthetic token (`syntheticHash`) for `downloadUrl`-only results. Season
   packs get the `AutoSelectFile` sentinel plus the requested season/episode
   baked into the signed URL. All TorrServer interaction (add, file-list poll,
   episode-file selection) happens at `/play`, once, for the torrent actually
   played, and the chosen file index is memoized in the `enrich_cache` table.

The signed `/play` payload binds `hash:index:season:episode:exp`; season/episode
are `0` for movies and exact episodes (their URL shape is unchanged).

## Local development

```bash
go test ./...                      # full offline suite
go vet ./...
go build -o addon ./cmd/addon      # static binary

# Run the server against real backends:
SIGNING_SECRET=dev \
PROWLARR_URL=http://localhost:9696 PROWLARR_API_KEY=xxxx \
TORRSERVER_URL=http://localhost:8090 \
DB_PATH=./dev.db \
./addon serve

# Admin CLI (same binary):
./addon adduser --name "Dev"
./addon listusers
./addon revoke --token <token>
```

All process settings come from the environment; see [`.env.example`](../.env.example)
for the full list. To add a new setting, extend `settings.Settings` +
`settings.Load`, thread it through `server.New`, and document it in
`.env.example`.

## Testing

- **Unit tests** are offline and hermetic — HTTP dependencies are mocked with
  `httptest`, and caches use an in-test no-op (`fakeCache`). Run `go test ./...`.

- **Live profiler.** `internal/resolver/profile_live_test.go` drives the full
  `Resolve` pipeline against a real Prowlarr + TorrServer and prints a per-stage
  timing breakdown plus result attrition (`raw → filtered → candidates →
  streams`). It is gated on `PROFILE_LIVE`, so it skips in normal runs. It must
  run where the services resolve — e.g. inside the deployment container, where
  `prowlarr` (Docker DNS) and TorrServer (`127.0.0.1:8090`) are reachable:

  ```bash
  # compile a static test binary, copy it into the running container, exec it
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o /tmp/resolver.test ./internal/resolver
  docker cp /tmp/resolver.test <container>:/tmp/resolver.test
  docker exec -e PROFILE_LIVE=1 <container> \
    /tmp/resolver.test -test.run TestProfileLiveResolve -test.v -test.timeout 180s
  ```

  Override the titles with `PROFILE_CASES="series|tt14688458:1:1|Silo;movie|tt1375666|Inception"`.

## Building images

```bash
# local, host arch:
docker build -t nuvio-torrserver:dev .

# multi-arch (what CI does):
docker buildx build --platform linux/amd64,linux/arm64 -t <ref> .
```

The Dockerfile cross-compiles the Go binary per target arch and selects the
matching TorrServer release asset automatically (`TARGETARCH` →
`TorrServer-linux-{amd64,arm64,arm7}`).

## Release / CI flow

`.github/workflows/docker-publish.yml`:

- **Every push / PR** runs `go vet` + `go test`.
- **Push to `main`** publishes `ghcr.io/<owner>/<repo>:latest` and a
  `:sha-<short>` tag (multi-arch).
- **Push a tag `vX.Y.Z`** additionally publishes `:X.Y.Z` and `:X.Y`.

So a release is just:

```bash
git tag v1.2.0 && git push origin v1.2.0
```

The workflow authenticates to GHCR with the built-in `GITHUB_TOKEN`
(`packages: write`); no extra secrets are required. The first published package
may need to be flipped to public (or granted to your users) in the repo's
**Packages** settings.

## Conventions

- Match the surrounding style; keep comments explaining *why*, not *what*.
- Keep `CGO_ENABLED=0` — no cgo dependencies.
- New caches live in the same SQLite DB (add a migration in `migrations/`), and
  should lazy-expire on read plus be reclaimed by `PurgeExpiredCache`.
- Prefer bounding external calls with a context deadline; a partial result beats
  a hung request.
