# skiptv-detector

Server-side intro and credits detection for a Plex library without Plex Pass.
SkipTV clients ask it for segments by Plex `ratingKey` and skip them.

## How it works

- Every `POLL_INTERVAL` (15 min) it lists movies and episodes from Plex
  (`/library/sections/*/all`) and stores path, size and duration in SQLite.
  A changed file (path or size) drops its cached analysis; items gone from
  Plex are removed. Only what Plex knows is analysed, never a disk walk.
- **Episodes.** For every episode ffmpeg decodes the first 25 % (≤ 10 min) and
  the last 7.5 min of one audio track (Russian if there is one, the one with
  the fewest channels) to 8 kHz mono. The audio is turned into a fingerprint:
  one 32-bit value every 32 ms from energy differences of 33 bands
  (300–3000 Hz) — the Haitsma–Kalker scheme, similar in spirit to
  chromaprint. Fingerprints are cached, so a new episode only costs its own
  decoding.
- Every pair of episodes of a season is aligned (shift voting over equal
  values, then the longest run of points differing in ≤ 6 bits with gaps
  ≤ 3.5 s). An intro is 15–120 s shared in the beginning, credits 15–450 s
  shared at the end. Each episode takes the range most of its siblings agree on
  (median start and end).
- **Credits by black frames**: for movies (last 20 min, at most the last
  quarter) and for episodes whose ending matched nothing, ffmpeg decodes only
  keyframes, scaled to 320 px, and credits start at the first keyframe that is
  ≥ 85 % dark and is followed by ≥ 75 % dark keyframes for a minute.
- Failed or empty decodes are not cached as results; an item is retried up to
  3 times (on later passes).
- One file at a time, ffmpeg under `nice`, `-threads 1`, capped probing. Before
  every ffmpeg run it checks the node's `MemAvailable` in VictoriaMetrics and
  waits while it's below `MIN_MEM_AVAILABLE_MB`.

## API

All `/v1` endpoints need the `X-Plex-Token` header: the token the client
uses for Plex. The detector checks it against the Plex server
(`/library/sections`) and caches the answer for 10 minutes (rejections for 1).

- `GET /v1/segments/{ratingKey}` →
  `{"ratingKey":"12345","segments":[{"type":"intro","start":17.5,"end":61.7},{"type":"credits","start":2909.1,"end":2987.9}]}`.
  Seconds from the start of the file, ordered by start. `type` is
  `intro|credits` (`recap|preview` are reserved). Nothing found → empty
  `segments`; not analysed yet (or unknown key) → also `"analyzed": false`.
- `GET /v1/segments?ratingKeys=1,2,3` (≤ 500) →
  `{"items":[<same object as above>, …]}` in the order asked.
- `GET /v1/stats` → counts of analysed items and found segments.
- `GET /healthz` → `ok`.

## Configuration (env)

| var | default | |
|---|---|---|
| `PLEX_URL` | `http://127.0.0.1:32400` | Plex server |
| `PLEX_TOKEN` | — | token with access to the libraries |
| `DB_PATH` | `/data/detector.db` | SQLite |
| `LISTEN` | `:8080` | |
| `POLL_INTERVAL` | `15m` | |
| `ANALYZE` | `true` | `false` serves the API only |
| `ONLY_PATH` | — | analyse only items whose path contains this |
| `VM_URL` | — | VictoriaMetrics for the memory guard |
| `VM_MEM_QUERY` | `node_memory_MemAvailable_bytes{node="raspberrypi5"}` | |
| `MIN_MEM_AVAILABLE_MB` | `400` | |
| `FFMPEG`, `FFMPEG_NICE`, `FFMPEG_TIMEOUT` | `ffmpeg`, `15`, `15m` | |
| `LOG_LEVEL` | `info` | `debug` logs every decode |

## Build

GitHub Actions (`.github/workflows/detector.yml`) runs `go vet`/`go test` and
builds `ghcr.io/emildeev/skiptv-detector` for arm64 (Alpine + ffmpeg). Tag
`detector-v<version>` publishes `:<version>`.

```bash
go test ./...
```
