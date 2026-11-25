package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TMDB's search in English and details in Russian, trimmed to what LocalName reads.
func fakeNames(t *testing.T, calls *atomic.Int32) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if q.Get("api_key") != "v3key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/3/search/movie":
			if q.Get("language") != "en-US" {
				t.Errorf("search in %q", q.Get("language"))
			}
			switch fold(q.Get("query")) { // TMDB's search ignores case
			case "soulm8te":
				w.Write([]byte(`{"results":[{"id":1307118,"title":"Soulm8te","original_title":"Soulm8te","release_date":"2026-01-09"}]}`))
			case "how to rob a bank":
				w.Write([]byte(`{"results":[
				 {"id":11,"title":"How to Rob a Bank","original_title":"How to Rob a Bank","release_date":"2007-07-08"},
				 {"id":1437511,"title":"How to Rob a Bank","original_title":"How to Rob a Bank","release_date":"2026-09-04"}]}`))
			case "twins":
				w.Write([]byte(`{"results":[
				 {"id":21,"title":"Twins","original_title":"Twins","release_date":"2020-01-01"},
				 {"id":22,"title":"Twins","original_title":"Twins","release_date":"2020-05-01"}]}`))
			case "late":
				w.Write([]byte(`{"results":[{"id":31,"title":"Late","original_title":"Late","release_date":"2025-12-30"}]}`))
			case "untranslated":
				w.Write([]byte(`{"results":[{"id":41,"title":"Untranslated","original_title":"Untranslated","release_date":"2026-02-02"}]}`))
			case "spirited away":
				w.Write([]byte(`{"results":[{"id":129,"title":"Spirited Away","original_title":"千と千尋の神隠し","release_date":"2001-07-20"}]}`))
			default:
				w.Write([]byte(`{"results":[]}`))
			}
		case "/3/search/tv":
			w.Write([]byte(`{"results":[{"id":70523,"name":"Dark","original_name":"Dark","first_air_date":"2017-12-01"}]}`))
		default:
			if q.Get("language") != "ru-RU" {
				t.Errorf("details in %q", q.Get("language"))
			}
			names := map[string]string{
				"/3/movie/1307118": `{"title":"СОУЛМ8ЙТ","original_title":"Soulm8te","release_date":"2026-01-09"}`,
				"/3/movie/1437511": `{"title":"Как ограбить банк","original_title":"How to Rob a Bank","release_date":"2026-09-04"}`,
				"/3/movie/31":      `{"title":"Опоздавший","original_title":"Late","release_date":"2025-12-30"}`,
				"/3/movie/41":      `{"title":"Untranslated","original_title":"Untranslated","release_date":"2026-02-02"}`,
				"/3/movie/129":     `{"title":"Унесённые призраками","original_title":"千と千尋の神隠し","release_date":"2001-07-20"}`,
				"/3/tv/70523":      `{"name":"Тьма","original_name":"Dark","first_air_date":"2017-12-01"}`,
			}
			body, ok := names[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write([]byte(body))
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL + "/3", Key: "v3key", HTTP: srv.Client()}
}

func TestLocalName(t *testing.T) {
	var calls atomic.Int32
	c := fakeNames(t, &calls)
	ctx := context.Background()
	cases := []struct {
		q    NameQuery
		want Name
	}{
		{NameQuery{Kind: Movie, Title: "Soulm8te", Year: 2026}, Name{TMDBID: 1307118, Title: "СОУЛМ8ЙТ"}},
		// Two films of that name: the year picks.
		{NameQuery{Kind: Movie, Title: "how to rob a  bank", Year: 2026}, Name{TMDBID: 1437511, Title: "Как ограбить банк"}},
		// Two of that name and year: not sure which, the English name stays.
		{NameQuery{Kind: Movie, Title: "Twins", Year: 2020}, Name{}},
		// One year off, and nothing else near.
		{NameQuery{Kind: Movie, Title: "Late", Year: 2026}, Name{TMDBID: 31, Title: "Опоздавший"}},
		// Found, but TMDB has no Russian name.
		{NameQuery{Kind: Movie, Title: "Untranslated", Year: 2026}, Name{TMDBID: 41}},
		// Plex's English name of a Japanese film.
		{NameQuery{Kind: Movie, Title: "Spirited Away", Year: 2001}, Name{TMDBID: 129, Title: "Унесённые призраками"}},
		{NameQuery{Kind: TV, Title: "Dark", Year: 2017}, Name{TMDBID: 70523, Title: "Тьма"}},
		// An id needs no search, nor a year.
		{NameQuery{Kind: TV, TMDBID: 70523}, Name{TMDBID: 70523, Title: "Тьма"}},
		// Without a year and an id nothing is searched.
		{NameQuery{Kind: Movie, Title: "Soulm8te"}, Name{}},
		{NameQuery{Kind: Movie, Title: "Nothing", Year: 2026}, Name{}},
		{NameQuery{Kind: Movie, TMDBID: 999}, Name{}},
	}
	for _, tc := range cases {
		got, err := c.LocalName(ctx, tc.q, "ru-RU")
		if err != nil {
			t.Fatalf("%+v: %v", tc.q, err)
		}
		if got != tc.want {
			t.Errorf("%+v: got %+v, want %+v", tc.q, got, tc.want)
		}
	}
	// Found and missed names come from the cache.
	before := calls.Load()
	for _, tc := range cases {
		c.LocalName(ctx, tc.q, "ru-RU")
	}
	if calls.Load() != before {
		t.Errorf("asked TMDB again: %d calls", calls.Load()-before)
	}
}

func TestLocalNameBadKey(t *testing.T) {
	var calls atomic.Int32
	c := fakeNames(t, &calls)
	c.Key = "wrong"
	if _, err := c.LocalName(context.Background(), NameQuery{Kind: Movie, Title: "Soulm8te", Year: 2026}, "ru-RU"); err != ErrBadKey {
		t.Fatalf("err %v", err)
	}
	// Not cached: a fixed key works at once.
	c.Key = "v3key"
	if got, _ := c.LocalName(context.Background(), NameQuery{Kind: Movie, Title: "Soulm8te", Year: 2026}, "ru-RU"); got.Title != "СОУЛМ8ЙТ" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseNameQuery(t *testing.T) {
	q, err := ParseNameQuery("movie:2026:0:Spider-Man: Beyond the Spider-Verse")
	if err != nil || q != (NameQuery{Kind: Movie, Year: 2026, Title: "Spider-Man: Beyond the Spider-Verse"}) {
		t.Fatalf("%+v %v", q, err)
	}
	if q, err := ParseNameQuery("tv:0:70523:"); err != nil || q.TMDBID != 70523 {
		t.Fatalf("%+v %v", q, err)
	}
	for _, bad := range []string{"movie:2026:Soulm8te", "person:0:0:x", "movie:x:0:y", "movie:2026:-1:y", "tv:0:0:", "movie:0:0: "} {
		if _, err := ParseNameQuery(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
