# Loge Server

The server part of Loge (formerly the Loge detector): intro and credits detection for a
Plex library without Plex Pass.
Loge clients (Apple TV, iPhone, iPad, Mac, Android, Android TV and
[web](https://github.com/loge-tv/web)) ask it for segments by Plex `ratingKey` and skip them.
Outside the home network it also hands out the video files themselves, which Plex without
Plex Pass refuses there.

## Install with Docker Compose

Loge Server runs next to Plex on a 64-bit Linux machine with Docker: x86-64 (`linux/amd64`) or
ARM64 (`linux/arm64`, e.g. a Raspberry Pi 4 or 5). It reads the same files Plex plays, so it
needs them on that machine, at the paths Plex shows for them. Plex on Windows shows Windows
paths (`D:\Movies\…`), which the server can't open yet.

1. Put [`compose.yaml`](compose.yaml) and [`.env.example`](.env.example) into a folder:

   ```bash
   mkdir loge-server && cd loge-server
   curl -fsSLO https://raw.githubusercontent.com/loge-tv/server/main/compose.yaml
   curl -fsSLO https://raw.githubusercontent.com/loge-tv/server/main/.env.example
   cp .env.example .env
   ```

2. Fill in `.env`:
   - `PLEX_URL`: your Plex server by its address in your home network, e.g.
     `http://192.168.1.10:32400`. Not `127.0.0.1` or `localhost`: in the container that is the
     container itself.
   - `PLEX_TOKEN`: the token of the Plex account that owns the server, see Plex's
     [Finding an authentication token](https://support.plex.tv/articles/204059436-finding-an-authentication-token-x-plex-token/).
     It stays on this machine; Loge apps send their own users' tokens, and the server checks
     them with Plex.
   - `MEDIA_DIR`: the folder with your movies and shows on this machine. It is mounted
     read-only.
   - `MEDIA_PATH_IN_PLEX`: only when Plex runs in Docker and shows the files under another path
     (in Plex: a movie's ⋯ menu, Get Info, the file's path). Then this is that path for the same
     folder, e.g. `/media` when Plex has `MEDIA_DIR` mounted at `/media`. A folder under `/data`
     (`/data/media`) is fine, `/data` itself is not: that is the database's volume.
   - More library folders: add a line per folder under `volumes:` in `compose.yaml`, in the
     same form.

3. Start it and watch the log:

   ```bash
   docker compose up -d
   docker compose logs -f
   ```

   `listening` means the API is up; every 15 minutes the server lists the library, and
   `pass done` ends an analysis pass. The first pass goes through the whole library one file at a
   time, at low priority: hours for a big one. Segments appear as items are done.

4. In each Loge app, set the Loge Server address to `http://<this machine's address>:8080`.
   For files outside the home network, the server must be reachable from there too, e.g. behind
   a reverse proxy with HTTPS.

The server runs as user 1000 in the container and must be able to read the files. When they are
readable only by a group, add it to the service in `compose.yaml`: `group_add: ["<group id>"]`.

`TMDB_API_KEY` is optional: your own [TMDB](https://www.themoviedb.org/) API key, for
`/v1/recommendations`, `/v1/videos` and `/v1/titles`. Without it those answer 404, and the apps
show no recommendations, trailers or translated catalog titles from this server.

The database (analysis results, the apps' per-show settings) lives in the Docker volume
`loge-server-data`, and `docker compose down` keeps it (`down -v` deletes it).

### Update

```bash
docker compose pull
docker compose up -d
```

`:main` is the latest build of the main branch. To stay on a release, set the image in
`compose.yaml` to its version, e.g. `ghcr.io/loge-tv/loge-server:0.5.0`.

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
- `GET /v1/recommendations?seeds=movie:38142,tv:1396[&language=ru-RU]` (≤ 5 seeds,
  TMDB ids) → TMDB's recommendations for each, for the clients' «Потому что вы смотрели…»
  shelf: `{"items":[{"seed":"movie:38142","titles":[{"tmdbId":372058,"type":"movie",
  "title":"Твоё имя","originalTitle":"君の名は。","releaseDate":"2016-08-26","year":2016}]}]}`,
  at most 20 per seed in TMDB's order, seeds in the order asked (duplicates dropped).
  Clients find the titles in the Plex catalog by `tmdb://<tmdbId>` themselves (with the
  account token the server never sees). An unknown id gives `"titles":[]`; a seed TMDB
  didn't answer gets `"failed":true`. Answers are cached for a day. Errors: 404
  `{"configured":false}` without `TMDB_API_KEY` (clients hide the shelf), 502 when TMDB
  refuses the key or no seed got an answer, 400 for bad seeds or language. No Plex token
  goes to TMDB.
- `GET /v1/videos?item=movie:603[&language=ru-RU]` (a TMDB id) → YouTube trailers and teasers
  of the movie or show for the clients' trailer button: `{"videos":[{"key":"<youtube id>",
  "name":"Трейлер","type":"Trailer","language":"ru","official":true,"publishedAt":"…",
  "size":1080}]}`, best first — in the language asked, then English, then the rest;
  trailers before teasers, official first, newest first. Cached for a day. TMDB lists few
  dubbed trailers (none for most new titles in Russian): when it has none in the language
  asked (Russian for now) and yt-dlp is there, YouTube's search is asked for
  «<title> <year> трейлер» (shows: «<title> сериал трейлер»), and up to two of its videos go
  first, `"official":false`, named «Трейлер, дубляж», «Тизер», «Трейлер, 2 сезон»… Only a
  video whose name has the title on its own (not «Книжный вор» for «Книжный»), a trailer word
  of the language and, when the name has one, the title's year (required when another title
  has the same name), 20 s – 5½ min long, not a review or a reaction, and playable embedded
  (YouTube's oEmbed). The search waits behind trailers someone pressed play on; a page waits
  for it at most 8 s. Found ones are kept a week, "none" a day. Errors like
  `/v1/recommendations`: 404 `{"configured":false}` without `TMDB_API_KEY`, 502 when TMDB
  refuses the key or doesn't answer, 400 for a bad item or language.
- `GET /v1/titles?item=movie:2026:0:Soulm8te&item=tv:0:70523:[&language=ru-RU]` (≤ 60 items,
  each `<movie|tv>:<year>:<tmdbId>:<title>`, year and id 0 when unknown) → names of catalog
  titles that Plex has only in English, from TMDB's translations, in the order asked:
  `{"items":[{"tmdbId":1307118,"title":"СОУЛМ8ЙТ"},{"tmdbId":70523,"title":"Тьма"},{}]}`.
  Without an id the title is searched on TMDB in English: same kind, same name (TMDB's English
  or original one, case aside) and the same year — one year off only when nothing else is
  near; two candidates leave it unknown (`{}`), since a wrong name is worse than an English
  one. No `title` when TMDB has no translation (for Russian: no Cyrillic name); `"failed":true`
  when TMDB didn't answer for that item (ask again). Found names are kept a month, misses a
  day. Errors like `/v1/recommendations`.
- `GET /v1/videos/{key}/stream` (a YouTube id from `/v1/videos`) → `{"url":"<HLS master
  playlist>","expiresAt":"…"}`: a trailer a TV plays itself (tvOS has no web view for YouTube's
  player). yt-dlp finds it (one at a time, then cached until shortly before YouTube lets it
  expire, ~6 h). yt-dlp stays loaded in a `python3` next to the server, so a new video takes
  ~2 s on a Raspberry Pi instead of ~5. If it can't be loaded, the server runs the program every
  time. YouTube binds the playlist to the server's public IP, so it plays only from the same
  network — clients ask only at home.
  - `?prefetch=1`: a page asks before anyone pressed play. Such requests wait behind the ones
    without it and are dropped if the client leaves before their turn.
  - Errors: 404 `{"configured":false}` without yt-dlp, 404 when the video has no HLS stream,
    502 when yt-dlp fails, 400 for a bad id.
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
| `TMDB_API_KEY` | — | TMDB v3 API key or v4 read access token; unset: no recommendations, trailers or titles (404) |
| `TMDB_URL` | `https://api.themoviedb.org/3` | |
| `YTDLP` | `yt-dlp` | yt-dlp for trailer streams (the image has it); `off`: no streams |
| `YTDLP_UPDATE` | `true` | `false`: don't run `yt-dlp -U` daily (on a copy in `/tmp`) |
| `TRAILER_SEARCH` | `true` | `false`: TMDB's trailers only, no YouTube search for ones in the language asked |
| `YTDLP_WORKER` | `true` | `false`: run yt-dlp as a program for every trailer instead of keeping it loaded in `python3` |
| `VM_MEM_QUERY` | `node_memory_MemAvailable_bytes{node="raspberrypi5"}` | |
| `MIN_MEM_AVAILABLE_MB` | `400` | |
| `FFMPEG`, `FFMPEG_NICE`, `FFMPEG_TIMEOUT` | `ffmpeg`, `15`, `15m` | |
| `LOG_LEVEL` | `info` | `debug` logs every decode |

## Build

GitHub Actions (`.github/workflows/server.yml`) runs `go vet`/`go test` on amd64 and arm64 and
builds `ghcr.io/loge-tv/loge-server` for both (`linux/amd64`, `linux/arm64`; Alpine + ffmpeg).
Releases up to 0.4.1 are arm64 only, and up to 0.3.1 they are `ghcr.io/loge-tv/detector`. Tag
`v<version>` publishes `:<version>`.

```bash
go test ./...
```

## TMDB from Russia

TMDB's DNS (Route 53 by location) answers `127.0.0.1` for Russian client subnets. Google and
Yandex DNS pass the client's subnet on (EDNS client subnet), so a router using them gives
`127.0.0.1` too; Cloudflare and Quad9 don't, and get the real addresses, which work. The
server resolves TMDB as usual and, when that gives only loopback addresses, asks
Cloudflare over DNS-over-HTTPS instead (`internal/tmdb/dial.go`).

## License

MIT, see [LICENSE](LICENSE).
