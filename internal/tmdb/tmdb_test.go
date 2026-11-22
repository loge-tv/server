package tmdb

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The shape of a real /movie/{id}/recommendations answer (trimmed).
const movieRecs = `{"page":1,"results":[
 {"adult":false,"id":372058,"title":"Твоё имя","original_title":"君の名は。","media_type":"movie","release_date":"2016-08-26","poster_path":"/q719jXXEzOoYaps6babgKnONONX.jpg"},
 {"adult":false,"id":129,"title":"Унесённые призраками","original_title":"千と千尋の神隠し","media_type":"movie","release_date":"2001-07-20"},
 {"adult":true,"id":1,"title":"x","media_type":"movie","release_date":"2001-01-01"},
 {"id":2,"name":"Person","media_type":"person"},
 {"adult":false,"id":3,"title":"Same","original_title":"Same","media_type":"movie","release_date":""}
],"total_pages":2,"total_results":40}`

const tvRecs = `{"page":1,"results":[
 {"adult":false,"id":1418,"name":"Теория большого взрыва","original_name":"The Big Bang Theory","media_type":"tv","first_air_date":"2007-09-24"}
]}`

func fake(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		key := r.URL.Query().Get("api_key")
		if auth := r.Header.Get("Authorization"); auth != "" {
			key = strings.TrimPrefix(auth, "Bearer ")
		}
		if key != "v3key" && key != "eyJv4token" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"status_code":7,"status_message":"Invalid API key"}`))
			return
		}
		if r.URL.Query().Get("language") != "ru-RU" || r.URL.Query().Get("page") != "1" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		switch r.URL.Path {
		case "/3/movie/38142/recommendations":
			w.Write([]byte(movieRecs))
		case "/3/tv/1396/recommendations":
			w.Write([]byte(tvRecs))
		case "/3/movie/500/recommendations":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"status_code":34}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRecommendations(t *testing.T) {
	var calls atomic.Int32
	srv := fake(t, &calls)
	c := New(srv.URL+"/3", "v3key")
	ctx := context.Background()

	got, err := c.Recommendations(ctx, Movie, 38142, "ru-RU")
	if err != nil {
		t.Fatal(err)
	}
	want := []Title{
		{TMDBID: 372058, Kind: Movie, Title: "Твоё имя", OriginalTitle: "君の名は。", ReleaseDate: "2016-08-26", Year: 2016},
		{TMDBID: 129, Kind: Movie, Title: "Унесённые призраками", OriginalTitle: "千と千尋の神隠し", ReleaseDate: "2001-07-20", Year: 2001},
		{TMDBID: 3, Kind: Movie, Title: "Same"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v want %+v", i, got[i], want[i])
		}
	}
	if _, err := c.Recommendations(ctx, Movie, 38142, "ru-RU"); err != nil || calls.Load() != 1 {
		t.Errorf("second ask: err %v, calls %d (cached: 1)", err, calls.Load())
	}

	tv, err := c.Recommendations(ctx, TV, 1396, "ru-RU")
	if err != nil || len(tv) != 1 || tv[0] != (Title{TMDBID: 1418, Kind: TV, Title: "Теория большого взрыва", OriginalTitle: "The Big Bang Theory", ReleaseDate: "2007-09-24", Year: 2007}) {
		t.Errorf("tv: %+v %v", tv, err)
	}
	none, err := c.Recommendations(ctx, Movie, 999, "ru-RU")
	if err != nil || len(none) != 0 {
		t.Errorf("unknown id: %+v %v", none, err)
	}
	if _, err := c.Recommendations(ctx, Movie, 500, "ru-RU"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("server error: %v", err)
	}
}

func TestKeyKinds(t *testing.T) {
	var calls atomic.Int32
	srv := fake(t, &calls)
	if _, err := New(srv.URL+"/3", "eyJv4token").Recommendations(context.Background(), TV, 1396, "ru-RU"); err != nil {
		t.Errorf("v4 token as bearer: %v", err)
	}
	if _, err := New(srv.URL+"/3", "wrong").Recommendations(context.Background(), TV, 1396, "ru-RU"); !errors.Is(err, ErrBadKey) {
		t.Errorf("bad key: %v", err)
	}
}

func TestUnreachableHidesKey(t *testing.T) {
	c := New("http://127.0.0.1:1/3", "secretkey")
	_, err := c.Recommendations(context.Background(), Movie, 1, "ru-RU")
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "secretkey") {
		t.Errorf("err %v", err)
	}
}

// TMDB's DNS answers 127.0.0.1 for Russian client subnets: the dialer then
// takes DNS-over-HTTPS addresses.
func TestResolveFallsBackOnLoopback(t *testing.T) {
	real := []net.IP{net.ParseIP("3.175.86.103")}
	var dohCalls int
	d := &Dialer{
		Lookup: func(_ context.Context, host string) ([]net.IP, error) {
			if host == "api.themoviedb.org" {
				return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, nil
			}
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		},
		DoH: func(_ context.Context, host string) ([]net.IP, error) {
			dohCalls++
			return real, nil
		},
	}
	ips, err := d.Resolve(context.Background(), "api.themoviedb.org")
	if err != nil || len(ips) != 1 || !ips[0].Equal(real[0]) {
		t.Errorf("blocked name: %v %v", ips, err)
	}
	ips, err = d.Resolve(context.Background(), "example.org")
	if err != nil || ips[0].String() != "93.184.216.34" || dohCalls != 1 {
		t.Errorf("normal name: %v %v, doh calls %d", ips, err, dohCalls)
	}
	ips, err = d.Resolve(context.Background(), "127.0.0.1")
	if err != nil || ips[0].String() != "127.0.0.1" || dohCalls != 1 {
		t.Errorf("literal address: %v %v", ips, err)
	}
	d.DoH = func(context.Context, string) ([]net.IP, error) { return nil, errors.New("down") }
	if _, err := d.Resolve(context.Background(), "api.themoviedb.org"); err == nil {
		t.Error("loopback only and no DoH: want an error")
	}
}
