package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The shape of a real /movie/{id}/videos answer (trimmed).
const movieVideos = `{"id":603,"results":[
 {"iso_639_1":"en","name":"Teaser EN","key":"teaserEN","site":"YouTube","size":1080,"type":"Teaser","official":true,"published_at":"2021-01-01T00:00:00.000Z"},
 {"iso_639_1":"en","name":"Trailer EN","key":"trailerEN","site":"YouTube","size":1080,"type":"Trailer","official":true,"published_at":"2020-01-01T00:00:00.000Z"},
 {"iso_639_1":"ru","name":"Трейлер (фан)","key":"fanRU","site":"YouTube","size":720,"type":"Trailer","official":false,"published_at":"2022-01-01T00:00:00.000Z"},
 {"iso_639_1":"ru","name":"Трейлер","key":"trailerRU","site":"YouTube","size":1080,"type":"Trailer","official":true,"published_at":"2019-01-01T00:00:00.000Z"},
 {"iso_639_1":"ru","name":"Featurette","key":"feat","site":"YouTube","type":"Featurette","official":true},
 {"iso_639_1":"ru","name":"Vimeo","key":"vimeo","site":"Vimeo","type":"Trailer","official":true},
 {"iso_639_1":"de","name":"Trailer DE","key":"trailerDE","site":"YouTube","type":"Trailer","official":true}
]}`

func TestVideos(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		switch r.URL.Path {
		case "/3/movie/603/videos":
			if q.Get("language") != "ru-RU" || q.Get("include_video_language") != "ru,en,null" {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			w.Write([]byte(movieVideos))
		case "/3/tv/1/videos":
			if q.Get("include_video_language") != "en,null" {
				t.Errorf("english: %s", r.URL.RawQuery)
			}
			w.Write([]byte(`{"results":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := New(srv.URL+"/3", "v3key")
	got, err := c.Videos(context.Background(), Movie, 603, "ru-RU")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, v := range got {
		keys = append(keys, v.Key)
	}
	want := []string{"trailerRU", "fanRU", "trailerEN", "teaserEN", "trailerDE"}
	if len(keys) != len(want) {
		t.Fatalf("got %v want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("got %v want %v", keys, want)
		}
	}
	if _, err := c.Videos(context.Background(), Movie, 603, "ru-RU"); err != nil || calls.Load() != 1 {
		t.Errorf("cached: err %v calls %d", err, calls.Load())
	}
	if v, err := c.Videos(context.Background(), TV, 1, "en-US"); err != nil || len(v) != 0 || v == nil {
		t.Errorf("none: %v %v", v, err)
	}
	if v, err := c.Videos(context.Background(), Movie, 999, "ru-RU"); err != nil || len(v) != 0 {
		t.Errorf("unknown id: %v %v", v, err)
	}
}

func TestDetailsAndSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("language") != "ru" {
			t.Errorf("language: %s", r.URL.RawQuery)
		}
		switch r.URL.Path {
		case "/3/movie/547009":
			w.Write([]byte(`{"title":"Зеленее травы","original_title":"Greener Grass","release_date":"2019-10-18"}`))
		case "/3/tv/97727":
			w.Write([]byte(`{"name":"Корпорация Заговор","original_name":"Inside Job","first_air_date":"2021-10-22","last_air_date":"2022-11-18"}`))
		case "/3/search/multi":
			if r.URL.Query().Get("query") != "Как ограбить банк" {
				t.Errorf("query: %s", r.URL.RawQuery)
			}
			w.Write([]byte(`{"results":[
			 {"id":1437511,"media_type":"movie","title":"Как ограбить банк","original_title":"How to Rob a Bank","release_date":"2026-09-04"},
			 {"id":1,"media_type":"person","name":"Someone"},
			 {"id":13534,"media_type":"movie","title":"Как ограбить банк","original_title":"How to Rob a Bank","release_date":"2007-01-01"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL + "/3", Key: "k", HTTP: srv.Client()}
	d, ok, err := c.Details(context.Background(), Movie, 547009, "ru")
	if err != nil || !ok || d.Title != "Зеленее травы" || d.Year != 2019 || d.LastYear != 2019 {
		t.Fatalf("%+v %v %v", d, ok, err)
	}
	d, ok, err = c.Details(context.Background(), TV, 97727, "ru")
	if err != nil || !ok || d.Title != "Корпорация Заговор" || d.Year != 2021 || d.LastYear != 2022 {
		t.Fatalf("%+v %v %v", d, ok, err)
	}
	if _, ok, err := c.Details(context.Background(), TV, 5, "ru"); ok || err != nil {
		t.Fatalf("unknown: %v %v", ok, err)
	}
	titles, err := c.Search(context.Background(), "Как ограбить банк", "ru")
	if err != nil || len(titles) != 2 || titles[1].TMDBID != 13534 || titles[1].Year != 2007 {
		t.Fatalf("%+v %v", titles, err)
	}
}
