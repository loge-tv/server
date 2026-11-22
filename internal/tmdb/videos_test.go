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
