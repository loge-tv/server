package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sepiatv/server/internal/store"
	"github.com/sepiatv/server/internal/tmdb"
)

type fakeRecs struct{ err map[int]error }

func (f fakeRecs) Recommendations(_ context.Context, kind tmdb.Kind, id int, language string) ([]tmdb.Title, error) {
	if err := f.err[id]; err != nil {
		return nil, err
	}
	if id == 404 {
		return nil, nil
	}
	return []tmdb.Title{{TMDBID: id * 10, Kind: kind, Title: language + " like " + string(kind), Year: 2020}}, nil
}

func setupRecs(t *testing.T, recs Recommender) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := httptest.NewServer((&Server{Store: st, Auth: &fakeAuth{}, Recs: recs, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		TTL: time.Minute, DenyTTL: time.Minute}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestRecommendations(t *testing.T) {
	srv := setupRecs(t, fakeRecs{err: map[int]error{7: tmdb.ErrUnavailable}})
	code, body := get(t, srv.URL+"/v1/recommendations?seeds=movie:603,tv:1396,movie:603,movie:404,movie:7", "good")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var out struct {
		Items []struct {
			Seed   string       `json:"seed"`
			Titles []tmdb.Title `json:"titles"`
			Failed bool         `json:"failed"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 4 {
		t.Fatalf("duplicates dropped, order kept: %s", body)
	}
	if out.Items[0].Seed != "movie:603" || out.Items[0].Titles[0].TMDBID != 6030 || out.Items[0].Titles[0].Title != "ru-RU like movie" {
		t.Errorf("first: %+v", out.Items[0])
	}
	if out.Items[1].Seed != "tv:1396" || out.Items[1].Titles[0].Kind != tmdb.TV {
		t.Errorf("tv: %+v", out.Items[1])
	}
	if out.Items[2].Titles == nil || len(out.Items[2].Titles) != 0 || out.Items[2].Failed {
		t.Errorf("unknown id: empty list, not failed: %s", body)
	}
	if !out.Items[3].Failed {
		t.Errorf("failed seed marked: %s", body)
	}

	if code, body := get(t, srv.URL+"/v1/recommendations?seeds=movie:603&language=en-US", "good"); code != 200 || !json.Valid([]byte(body)) || !contains(body, "en-US like movie") {
		t.Errorf("language: %d %s", code, body)
	}
	for _, q := range []string{"", "seeds=", "seeds=show:1", "seeds=movie:x", "seeds=movie:1,movie:2,movie:3,movie:4,movie:5,movie:6", "seeds=movie:1&language=russian"} {
		if code, body := get(t, srv.URL+"/v1/recommendations?"+q, "good"); code != 400 {
			t.Errorf("%q: %d %s", q, code, body)
		}
	}
	if code, _ := get(t, srv.URL+"/v1/recommendations?seeds=movie:603", ""); code != 401 {
		t.Errorf("no token: %d", code)
	}
}

func TestRecommendationsErrors(t *testing.T) {
	if code, body := get(t, setupRecs(t, nil).URL+"/v1/recommendations?seeds=movie:603", "good"); code != 404 || !contains(body, `"configured":false`) {
		t.Errorf("no key: %d %s", code, body)
	}
	srv := setupRecs(t, fakeRecs{err: map[int]error{1: tmdb.ErrUnavailable, 2: tmdb.ErrUnavailable, 3: tmdb.ErrBadKey}})
	if code, body := get(t, srv.URL+"/v1/recommendations?seeds=movie:1,tv:2", "good"); code != 502 || !contains(body, "unreachable") {
		t.Errorf("all failed: %d %s", code, body)
	}
	if code, body := get(t, srv.URL+"/v1/recommendations?seeds=movie:603,movie:3", "good"); code != 502 || !contains(body, "api key") {
		t.Errorf("bad key: %d %s", code, body)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

type fakeVideos struct{}

func (fakeVideos) Videos(_ context.Context, kind tmdb.Kind, id int, language string) ([]tmdb.Video, error) {
	if id == 7 {
		return nil, tmdb.ErrUnavailable
	}
	return []tmdb.Video{{Key: language + "-" + string(kind), Type: "Trailer", Language: language[:2], Official: true}}, nil
}

func TestVideos(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := httptest.NewServer((&Server{Store: st, Auth: &fakeAuth{}, Videos: fakeVideos{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		TTL: time.Minute, DenyTTL: time.Minute}).Handler())
	defer srv.Close()
	if code, body := get(t, srv.URL+"/v1/videos?item=movie:603", "good"); code != 200 || !contains(body, `"key":"ru-RU-movie"`) {
		t.Errorf("default language: %d %s", code, body)
	}
	if code, body := get(t, srv.URL+"/v1/videos?item=tv:1396&language=en-US", "good"); code != 200 || !contains(body, `"key":"en-US-tv"`) {
		t.Errorf("tv: %d %s", code, body)
	}
	for _, q := range []string{"", "item=show:1", "item=movie:x", "item=movie:1&language=russian"} {
		if code, body := get(t, srv.URL+"/v1/videos?"+q, "good"); code != 400 {
			t.Errorf("%q: %d %s", q, code, body)
		}
	}
	if code, _ := get(t, srv.URL+"/v1/videos?item=movie:7", "good"); code != 502 {
		t.Errorf("tmdb down: %d", code)
	}
	if code, _ := get(t, srv.URL+"/v1/videos?item=movie:603", ""); code != 401 {
		t.Errorf("no token: %d", code)
	}
	if code, body := get(t, setupRecs(t, nil).URL+"/v1/videos?item=movie:603", "good"); code != 404 || !contains(body, `"configured":false`) {
		t.Errorf("no key: %d %s", code, body)
	}
}
