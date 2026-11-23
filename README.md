# Loge Server

The server part of Loge (formerly the Loge detector): intro and credits detection for a
Plex library without Plex Pass.
Loge clients (Apple TV, iPhone, Mac, Android and
[web](https://github.com/loge-tv/web)) ask it for segments by Plex `ratingKey` and skip them.
Outside the home network it also hands out the video files themselves, which Plex without
Plex Pass refuses there.

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
  ≥ 85 % dark and is followed by ≥ 75 % dark keyframes for a minute (or until
  the end of the file, if that is closer — sitcom credits are ~25 s). They end
  where a scene after them starts (mid/post-credits): ≥ 10 s of keyframes
  under 50 % dark, outside the last 15 s (studio logos), whose picture is
  textured — the median 10th–90th luma percentile spread of 30 s of keyframes
  is ≥ 8 % (credits on a colour background give ~3 %). The start is checked
  the same way: on black, the 10th luma percentile (8 bit gray) of ≥ 60 % of
  30 s of keyframes is ≤ 3 (credits give 0–2) — or, in encodes that keep
  black at ~15 and never go lower, the same value frame after frame; a dark
  ending of the film wanders at 4–9 and is skipped for the next candidate.
- **Credits by text**: the same keyframes (320×180 gray) are searched for
  text lines — bands 2–12 rows high with many sharp luma steps, per quarter of
  the width, so credits beside a picture count too. A line counts as text
  when it is ≥ 20 columns wide (strokes with gaps of ≤ 4 columns, measured
  over the whole width): credits give 20–200, garlands and lamps in a scene
  are rows of dots 4–20 wide (Black-ish 7x03). Credits text is ≥ 4 such lines
  that moved since the neighbouring keyframe (scrolled or a new card): signs,
  logos and watermarks stay put. Credits start at the first such keyframe
  followed by ≥ 60 % of them for 30 s. Of the text and the black start the
  earlier wins, unless its credits end at a scene before the later one.
  Keyframes with ≥ 4 text lines are never a scene after the credits. Sparse
  1–2-line cards on black are left to the black frames.
- A movie's end scan uses the file's own duration (ffprobe) when Plex reports
  a shorter one (Kraven .m2ts: 1094 s of 7616).
- Failed or empty decodes are not cached as results; an item is retried up to
  3 times (on later passes); a new `analyzer.Version` gets 3 tries of its own.
  A file Plex lists but the pod can't read (the SSD dropped out) costs no
  attempt: the item stays pending until the file is back.
- An end scan is cached with the file's size and mtime and redone when they
  change, or when it stops > 2 min before the duration (ffmpeg takes a read
  error for the end of the file).
- Plex first reports a rough duration (whole minutes) for new files. When it
  grows later, the item is re-analysed, and cached windows that stop short of
  the new end are decoded again. Bumping `analyzer.Version` recomputes all
  results from the caches (a minute, no ffmpeg).
- `loge-server segments` (in the pod) dumps every item with its segments as TSV,
  to diff results before and after a change; `loge-server frames <key> <from>
  <to>` prints the cached end scan keyframes, `loge-server scan <key> <from>
  <to>` decodes them now (nothing stored).
- One file at a time, ffmpeg under `nice`, `-threads 1`, capped probing. Before
  every ffmpeg run it checks the node's `MemAvailable` in VictoriaMetrics and
  waits while it's below `MIN_MEM_AVAILABLE_MB`.

## API

All `/v1` endpoints need the `X-Plex-Token` header: the token the client
uses for Plex (`/v1/files` also takes it as the `X-Plex-Token` query parameter,
for a browser's `<video src>`). The server checks it against the Plex server
(`/library/sections`) and caches the answer for 10 minutes (rejections for 1).

- `GET /v1/segments/{ratingKey}` →
  `{"ratingKey":"12345","segments":[{"type":"intro","start":17.5,"end":61.7},{"type":"credits","start":2909.1,"end":2987.9}]}`.
  Seconds from the start of the file, ordered by start. `type` is
  `intro|credits` (`recap|preview` are reserved). Nothing found → empty
  `segments`; not analysed yet (or unknown key) → also `"analyzed": false`.
- `GET /v1/segments?ratingKeys=1,2,3` (≤ 500) →
  `{"items":[<same object as above>, …]}` in the order asked.
- `GET /v1/stats` → counts of analysed items and found segments.
- Client settings per show (its ratingKey) or movie — an opaque JSON object
  the server stores as is (own table, untouched by library sync):
  - `GET /v1/preferences/{key}` →
    `{"key":"10","preferences":{…},"updatedAt":"2026-11-21T20:00:00Z"}`, no
    record → `{"key":"10","preferences":null}`;
  - `PUT /v1/preferences/{key}` with a JSON object (≤ 16 KiB) → stores it and
    answers like `GET`; `{}` or `null` delete the record; anything else → 400;
  - `GET /v1/preferences` → `{"items":[…]}`, all records.
- CORS for the web client on every `/v1/*`: `Access-Control-Allow-Origin: *`
  (auth is a header, no cookies), methods `GET, HEAD, PUT, OPTIONS`, headers
  `X-Plex-Token, Content-Type, Accept, Range`, exposed `Content-Length,
  Content-Range, Accept-Ranges`, max age a day; a preflight `OPTIONS` gets 204
  without a token check.
- `GET /v1/files/{ratingKey}/{partId}[/{name}]` (and `HEAD`) → the original file of
  that part (`Media[].Part[].id`), with `Range` for seeking; `{name}` is ignored
  (players like `file.mkv`). Outside the home network Plex without Plex Pass answers
  the file itself (`/library/parts/…`) with 403 "Downloading requires a Remote Watch
  Pass or Plex Pass", while lists and artwork still load; the server has the same
  library mounted (read-only, same path as Plex) and serves it instead. The path
  comes from Plex — `/library/metadata/{ratingKey}` asked with the client's token,
  so the token must see that very item — and never from the request; it is cached
  per token for 10 minutes (a player reopens the file on every seek). Errors: 403
  the token can't see the item, 404 no such item or part, 503 the file can't be
  opened (the disk dropped out), 502 Plex is down.
- `GET /healthz` → `ok`.

## Configuration (env)

| var | default | |
|---|---|---|
| `PLEX_URL` | `http://127.0.0.1:32400` | Plex server |
| `PLEX_TOKEN` | — | token with access to the libraries |
| `DB_PATH` | `/data/detector.db` | SQLite (the old file name, kept so existing installs keep their data) |
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

GitHub Actions (`.github/workflows/server.yml`) runs `go vet`/`go test` and
builds `ghcr.io/loge-tv/loge-server` for arm64 (Alpine + ffmpeg); releases up to 0.3.1 are
`ghcr.io/loge-tv/detector`. Tag
`v<version>` publishes `:<version>`.

```bash
go test ./...
```

## License

MIT, see [LICENSE](LICENSE).
