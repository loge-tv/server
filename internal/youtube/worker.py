# Loge Server's yt-dlp, loaded once: a video id per line in, one JSON line out.
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
print(json.dumps({"ready": yt_dlp.version.__version__}), flush=True)

for line in sys.stdin:
    key = line.strip()
    if not key:
        continue
    try:
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
