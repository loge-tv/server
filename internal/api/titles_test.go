package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/sepiatv/server/internal/store"
	"github.com/sepiatv/server/internal/tmdb"
)

type fakeNamer struct{ asked []string }

func (f *fakeNamer) LocalName(_ context.Context, q tmdb.NameQuery, language string) (tmdb.Name, error) {
	switch q.Title {
	case "Soulm8te":
		return tmdb.Name{TMDBID: 1307118, Title: language + " СОУЛМ8ЙТ"}, nil
	case "Down":
		return tmdb.Name{}, tmdb.ErrUnavailable
	case "BadKey":
		return tmdb.Name{}, tmdb.ErrBadKey
	}
	return tmdb.Name{TMDBID: q.TMDBID}, nil
}

func setupNames(t *testing.T, names Namer) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Server{Store: st, Auth: &fakeAuth{}, Log: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})), TTL: time.Minute, DenyTTL: time.Minute}
	if names != nil {
		s.Names = names
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func titlesURL(base string, items ...string) string {
	v := url.Values{"item": items}
	return base + "/v1/titles?" + v.Encode()
}

func TestTitles(t *testing.T) {
	srv := setupNames(t, &fakeNamer{})
	code, body := get(t, titlesURL(srv.URL, "movie:2026:0:Soulm8te", "tv:0:70523:", "movie:2026:0:Down"), "good")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var out struct {
		Items []struct {
			TMDBID int    `json:"tmdbId"`
			Title  string `json:"title"`
			Failed bool   `json:"failed"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 3 || out.Items[0].Title != "ru-RU СОУЛМ8ЙТ" || out.Items[0].TMDBID != 1307118 ||
		out.Items[1].Title != "" || out.Items[1].TMDBID != 70523 || !out.Items[2].Failed {
		t.Fatalf("in order, failed marked: %s", body)
	}

	if code, _ := get(t, titlesURL(srv.URL, "movie:2026:0:Soulm8te"), ""); code != 401 {
		t.Errorf("no token: %d", code)
	}
	for _, bad := range []string{
		srv.URL + "/v1/titles",
		titlesURL(srv.URL, "movie:2026:Soulm8te"),
		titlesURL(srv.URL, "movie:2026:0:Soulm8te") + "&language=russian",
	} {
		if code, body := get(t, bad, "good"); code != 400 {
			t.Errorf("%s: %d %s", bad, code, body)
		}
	}
	many := make([]string, maxNameItems+1)
	for i := range many {
		many[i] = "movie:2026:0:x"
	}
	if code, _ := get(t, titlesURL(srv.URL, many...), "good"); code != 400 {
		t.Errorf("too many: %d", code)
	}
	if code, _ := get(t, titlesURL(srv.URL, "movie:2026:0:Down"), "good"); code != 502 {
		t.Errorf("all failed: %d", code)
	}
	if code, _ := get(t, titlesURL(srv.URL, "movie:2026:0:BadKey", "movie:2026:0:Soulm8te"), "good"); code != 502 {
		t.Errorf("bad key: %d", code)
	}
}

func TestTitlesWithoutTMDB(t *testing.T) {
	srv := setupNames(t, nil)
	if code, _ := get(t, titlesURL(srv.URL, "movie:2026:0:Soulm8te"), "good"); code != 404 {
		t.Errorf("no key: %d", code)
	}
}
