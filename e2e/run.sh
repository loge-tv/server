#!/bin/bash
# Compose smoke test: the tracked compose.yaml with e2e/compose.e2e.yaml. A
# generated season (3 episodes, a shared intro and shared credits, tones
# everywhere else) goes through a fake Plex; the analysis runs RUNS times on a
# fresh volume and must never crash. The first run also checks auth,
# /v1/files with Range, TMDB with a key (fake TMDB) and without one (404).
#
# Usage: e2e/run.sh [RUNS]   image: $E2E_IMAGE (default loge-server:e2e)
#
# segments.json is what the analyzer finds today, compared within 0.5 s:
# starts and credits are within 1 s of the generated ones, intro ends run
# ~7 s long on these tones.
set -uo pipefail
cd "$(dirname "$0")/.."
RUNS=${1:-1}
export E2E_IMAGE=${E2E_IMAGE:-loge-server:e2e}
export PLEX_URL=http://fakeplex:32400 PLEX_TOKEN=e2e-plex-token MEDIA_DIR=./e2e/media MEDIA_PATH_IN_PLEX=/media
API=http://127.0.0.1:8080
T="X-Plex-Token: $PLEX_TOKEN"

dc() { docker compose -p loge-e2e -f compose.yaml -f e2e/compose.e2e.yaml "$@"; }
fail() {
	echo "FAIL: $*"
	dc logs --no-color loge-server | tail -60
	dc down -v >/dev/null 2>&1
	exit 1
}
crashed() {
	local logs status
	logs=$(dc logs --no-color loge-server 2>&1)
	status=$(docker inspect -f '{{.State.Status}} {{.RestartCount}}' "$(dc ps -aq loge-server)" 2>/dev/null)
	if grep -q -E 'panic:|fatal error' <<<"$logs" || [ "$status" != "running 0" ]; then
		echo "server: $status"
		return 0
	fi
	return 1
}
# expect <what> <code> <body substring> <curl args…>
expect() {
	local what=$1 code=$2 want=$3 got
	shift 3
	got=$(curl -s -o /tmp/loge-e2e-body -w '%{http_code}' "$@")
	[ "$got" = "$code" ] && grep -q -F -- "$want" /tmp/loge-e2e-body ||
		fail "$what: HTTP $got $(head -c 300 /tmp/loge-e2e-body), want $code with $want"
	echo "ok: $what"
}

if [ ! -f e2e/media/manifest.json ]; then
	mkdir -p e2e/media
	docker run --rm --entrypoint python3 -u "$(id -u):$(id -g)" -v "$PWD/e2e:/e2e" "$E2E_IMAGE" \
		-I /e2e/gen_media.py /e2e/media /e2e/media/manifest.json || fail "generating the season"
fi

for n in $(seq 1 "$RUNS"); do
	dc down -v >/dev/null 2>&1
	TMDB_API_KEY=e2e-tmdb-key dc up -d >/dev/null 2>&1 || fail "run $n: compose up"
	done=""
	for _ in $(seq 1 120); do
		crashed && fail "run $n: the server crashed"
		dc logs --no-color loge-server 2>&1 | grep -q 'pass done' && done=1 && break
		sleep 2
	done
	[ -n "$done" ] || fail "run $n: no analysis pass in 4 minutes"
	curl -sf -H "$T" "$API/v1/segments?ratingKeys=101,102,103" >/tmp/loge-e2e-segments || fail "run $n: segments"
	python3 - /tmp/loge-e2e-segments e2e/segments.json <<-'EOF' || fail "run $n: segments differ from e2e/segments.json"
		import json, sys
		got, want = (json.load(open(p))["items"] for p in sys.argv[1:])
		def flat(items):
		    return [(i["ratingKey"], s["type"], s["start"], s["end"]) for i in items for s in i["segments"]]
		g, w = flat(got), flat(want)
		ok = len(g) == len(w) and all(a[:2] == b[:2] and abs(a[2] - b[2]) <= 0.5 and abs(a[3] - b[3]) <= 0.5 for a, b in zip(g, w))
		if not ok:
		    print("got", json.dumps(got))
		sys.exit(0 if ok else 1)
	EOF
	echo "ok: run $n analysed, segments as expected"

	if [ "$n" = 1 ]; then
		expect "segments, wrong token" 401 "" -H "X-Plex-Token: nope" "$API/v1/segments/101"
		expect "segments, no token" 401 "" "$API/v1/segments/101"
		expect "files, Range" 206 "" -r 1000-1999 -H "$T" "$API/v1/files/101/201/file.mkv"
		cmp -s /tmp/loge-e2e-body <(tail -c +1001 "e2e/media/Test Show/Season 01/Test Show - S01E01.mkv" | head -c 1000) ||
			fail "files, Range: not the file's bytes"
		expect "files, token in the query" 206 "" -r 0-9 "$API/v1/files/101/201?X-Plex-Token=$PLEX_TOKEN"
		expect "files, wrong part" 404 "" -H "$T" "$API/v1/files/101/999"
		expect "files, wrong token" 401 "" "$API/v1/files/101/201?X-Plex-Token=nope"
		expect "recommendations" 200 '"tmdbId":604' -H "$T" "$API/v1/recommendations?seeds=movie:603&language=ru-RU"
		expect "videos" 200 '"key":"vKQi3bBA1y8"' -H "$T" "$API/v1/videos?item=movie:603&language=en-US"
		expect "titles" 200 '"title":"Тьма"' -H "$T" "$API/v1/titles?item=tv:0:70523:Dark&language=ru-RU"
		expect "stream, no yt-dlp" 404 '"configured":false' -H "$T" "$API/v1/videos/vKQi3bBA1y8/stream"

		TMDB_API_KEY= dc up -d loge-server >/dev/null 2>&1 || fail "compose up without a key"
		for _ in $(seq 1 30); do curl -sf "$API/healthz" >/dev/null && break; sleep 1; done
		expect "recommendations, no key" 404 '"configured":false' -H "$T" "$API/v1/recommendations?seeds=movie:603"
		expect "videos, no key" 404 '"configured":false' -H "$T" "$API/v1/videos?item=movie:603"
		expect "titles, no key" 404 '"configured":false' -H "$T" "$API/v1/titles?item=tv:0:70523:Dark"
		expect "segments, kept in the volume" 200 '"type":"intro"' -H "$T" "$API/v1/segments/102"
		crashed && fail "the server crashed"
	fi
done
dc down -v >/dev/null 2>&1
echo "OK: $RUNS runs on $(uname -m)"
