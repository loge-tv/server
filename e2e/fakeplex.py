"""A Plex stand-in for Sepia Server tests: one show library with the episodes of
manifest.json. Only the token in PLEX_TOKEN gets in (others: 401).

Usage: python3 -I fakeplex.py <manifest.json> [port]
"""
import json
import os
import re
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit, parse_qs

ITEMS = json.load(open(sys.argv[1]))
TOKEN = os.environ["PLEX_TOKEN"]


def metadata(it, streams):
    part = {"id": it["partId"], "file": it["file"], "size": it["size"], "duration": it["duration"]}
    if streams:
        part["Stream"] = [
            {"id": 1000 + it["partId"], "streamType": 1, "index": 0, "codec": "h264"},
            {"id": 2000 + it["partId"], "streamType": 2, "index": 1, "codec": "flac",
             "channels": 1, "languageCode": "rus", "language": "Русский", "default": True},
        ]
    return {
        "ratingKey": it["ratingKey"], "type": "episode", "title": it["title"],
        "grandparentTitle": "Test Show", "parentRatingKey": "100", "parentIndex": 1,
        "index": it["index"], "duration": it["duration"], "updatedAt": 1795500000,
        "Media": [{"duration": it["duration"], "Part": [part]}],
    }


class H(BaseHTTPRequestHandler):
    def send(self, code, body):
        data = json.dumps({"MediaContainer": body}).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        u = urlsplit(self.path)
        token = self.headers.get("X-Plex-Token") or parse_qs(u.query).get("X-Plex-Token", [""])[0]
        if token != TOKEN:
            return self.send(401, {})
        if u.path == "/":
            return self.send(200, {"machineIdentifier": "e2e-machine-id", "friendlyName": "E2E Plex"})
        if u.path == "/library/sections":
            return self.send(200, {"Directory": [{"key": "2", "type": "show", "title": "Shows"}]})
        if u.path == "/library/sections/2/all":
            return self.send(200, {"Metadata": [metadata(it, False) for it in ITEMS]})
        m = re.fullmatch(r"/library/metadata/(\w+)", u.path)
        if m:
            found = [metadata(it, True) for it in ITEMS if it["ratingKey"] == m.group(1)]
            return self.send(200 if found else 404, {"Metadata": found})
        self.send(404, {})


if __name__ == "__main__":
    ThreadingHTTPServer(("", int(sys.argv[2]) if len(sys.argv) > 2 else 32400), H).serve_forever()
