# Sepia Server's yt-dlp, loaded once: a video id (or a JSON search request) per
# line in, one JSON line out.
# Started by worker.go with the yt-dlp zipapp (importable) as its argument; a
# new yt-dlp process per trailer spent ~2 s of a Raspberry Pi on starting.
import json
import sys

sys.path.insert(0, sys.argv[1])
import yt_dlp  # noqa: E402

ydl = yt_dlp.YoutubeDL({
    "quiet": True,
    "no_warnings": True,
    "noplaylist": True,
    "skip_download": True,
    "cachedir": False,
})
searchers = {}


def searcher(lang):
    """Search results only (no video pages), titles in lang when the uploader translated them."""
    if lang not in searchers:
        params = {
            "quiet": True,
            "no_warnings": True,
            "skip_download": True,
            "cachedir": False,
            "extract_flat": "in_playlist",
        }
        if lang:
            params["extractor_args"] = {"youtube": {"lang": [lang]}}
        searchers[lang] = yt_dlp.YoutubeDL(params)
    return searchers[lang]


def search(request):
    n = max(1, min(int(request.get("n") or 10), 20))
    query = " ".join(str(request.get("search") or "").split())
    info = searcher(str(request.get("lang") or "")).extract_info(f"ytsearch{n}:{query}", download=False)
    return {
        "entries": [
            {
                "id": e.get("id") or "",
                "title": e.get("title") or "",
                "duration": e.get("duration") or 0,
                "channel": e.get("channel") or e.get("uploader") or "",
            }
            for e in info.get("entries") or []
            if e.get("id")
        ]
    }


print(json.dumps({"ready": yt_dlp.version.__version__}), flush=True)

for line in sys.stdin:
    key = line.strip()
    if not key:
        continue
    try:
        if key.startswith("{"):
            print(json.dumps(search(json.loads(key))), flush=True)
            continue
        # Unprocessed: the formats as YouTube lists them, manifest URLs included.
        info = ydl.extract_info("https://www.youtube.com/watch?v=" + key, download=False, process=False)
        formats = [
            {"protocol": f.get("protocol") or "", "manifest_url": f["manifest_url"]}
            for f in info.get("formats") or []
            if f.get("manifest_url")
        ]
        print(json.dumps({"formats": formats}), flush=True)
    except Exception as e:  # noqa: BLE001 — every failure is an answer, the loop goes on
        lines = str(e).strip().splitlines()
        print(json.dumps({"error": lines[-1] if lines else type(e).__name__}), flush=True)
