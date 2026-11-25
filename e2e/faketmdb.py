"""A TMDB v3 stand-in for Loge Server tests: a few canned answers; only the
api_key in TMDB_KEY gets in (others: 401, like TMDB). Logs every path asked.

Usage: python3 -I faketmdb.py [port]
"""
import json
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit, parse_qs

KEY = os.environ["TMDB_KEY"]

ANSWERS = {
    "/3/movie/603/recommendations": {"results": [
        {"id": 604, "media_type": "movie", "title": "Матрица: Перезагрузка",
         "original_title": "The Matrix Reloaded", "release_date": "2003-05-15"},
    ]},
    "/3/movie/603/videos": {"results": [
        {"key": "vKQi3bBA1y8", "name": "The Matrix Trailer", "site": "YouTube", "type": "Trailer",
         "iso_639_1": "en", "official": True, "published_at": "2016-08-16T00:00:00.000Z", "size": 1080},
    ]},
    "/3/tv/70523": {"name": "Тьма", "original_name": "Dark",
                    "first_air_date": "2017-12-01", "last_air_date": "2020-06-27"},
}


class H(BaseHTTPRequestHandler):
    def send(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        u = urlsplit(self.path)
        q = parse_qs(u.query)
        sys.stdout.write(f"GET {u.path} language={q.get('language', [''])[0]}\n")
        sys.stdout.flush()
        if q.get("api_key", [""])[0] != KEY:
            return self.send(401, {"status_code": 7, "status_message": "Invalid API key"})
        if u.path in ANSWERS:
            return self.send(200, ANSWERS[u.path])
        self.send(404, {"status_code": 34, "status_message": "The resource you requested could not be found."})

    def log_message(self, *a):
        pass


if __name__ == "__main__":
    ThreadingHTTPServer(("", int(sys.argv[1]) if len(sys.argv) > 1 else 80), H).serve_forever()
