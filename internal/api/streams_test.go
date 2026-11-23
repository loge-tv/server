package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/loge-tv/detector/internal/store"
	"github.com/loge-tv/detector/internal/youtube"
)

type fakeStreams struct{}

func (fakeStreams) Stream(_ context.Context, key string) (youtube.Stream, error) {
	switch key {
	case "nostream1":
		return youtube.Stream{}, youtube.ErrNoStream
	case "brokenyt1":
		return youtube.Stream{}, youtube.ErrUnavailable
	}
	return youtube.Stream{URL: "https://manifest.googlevideo.com/" + key + ".m3u8", Expires: time.Unix(1795442435, 0).UTC()}, nil
}

func setupStreams(t *testing.T, streams StreamFinder) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := httptest.NewServer((&Server{Store: st, Auth: &fakeAuth{}, Streams: streams, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		TTL: time.Minute, DenyTTL: time.Minute}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestStream(t *testing.T) {
	srv := setupStreams(t, fakeStreams{})
	code, body := get(t, srv.URL+"/v1/videos/AyIZ9tiiN8I/stream", "good")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var out youtube.Stream
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.URL != "https://manifest.googlevideo.com/AyIZ9tiiN8I.m3u8" || out.Expires.IsZero() {
		t.Fatalf("%v %s", err, body)
	}
	for path, want := range map[string]int{
		"/v1/videos/nostream1/stream": 404,
		"/v1/videos/brokenyt1/stream": 502,
		"/v1/videos/a%20b%3Bc/stream": 400,
	} {
		if code, body := get(t, srv.URL+path, "good"); code != want {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
	if code, _ := get(t, srv.URL+"/v1/videos/AyIZ9tiiN8I/stream", ""); code != 401 {
		t.Errorf("no token: %d", code)
	}
}

func TestStreamWithoutYtDlp(t *testing.T) {
	srv := setupStreams(t, nil)
	code, body := get(t, srv.URL+"/v1/videos/AyIZ9tiiN8I/stream", "good")
	if code != 404 || !json.Valid([]byte(body)) {
		t.Fatalf("%d %s", code, body)
	}
}
