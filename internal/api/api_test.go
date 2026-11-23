package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loge-tv/server/internal/store"
)

type fakeAuth struct{ calls int }

func (f *fakeAuth) CheckAccess(_ context.Context, token string) (bool, error) {
	f.calls++
	return token == "good" || token == "limited", nil
}

func setup(t *testing.T) (*httptest.Server, *fakeAuth) {
	t.Helper()
	return setupWith(t, nil)
}

func setupWith(t *testing.T, files FileFinder) (*httptest.Server, *fakeAuth) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	for _, k := range []string{"1", "2"} {
		if err := st.UpsertItem(ctx, store.Item{RatingKey: k, Kind: "episode", Path: "/x/" + k, Size: 1, Duration: 3000}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetResult(ctx, "1", 1, []store.Segment{
		{Type: "credits", Start: 2909.1, End: 2987.94, Source: "fingerprint"},
		{Type: "intro", Start: 17.5, End: 61.7, Source: "fingerprint"},
	}); err != nil {
		t.Fatal(err)
	}
	fa := &fakeAuth{}
	srv := httptest.NewServer((&Server{Store: st, Auth: fa, Files: files, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		TTL: time.Minute, DenyTTL: time.Minute}).Handler())
	t.Cleanup(srv.Close)
	return srv, fa
}

func get(t *testing.T, url, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("X-Plex-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func compact(t *testing.T, s string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad json %q: %v", s, err)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func TestSegments(t *testing.T) {
	srv, fa := setup(t)
	cases := []struct{ path, want string }{
		{"/v1/segments/1", `{"ratingKey":"1","segments":[{"end":61.7,"start":17.5,"type":"intro"},{"end":2987.9,"start":2909.1,"type":"credits"}]}`},
		{"/v1/segments/2", `{"analyzed":false,"ratingKey":"2","segments":[]}`},
		{"/v1/segments/404", `{"analyzed":false,"ratingKey":"404","segments":[]}`},
		{"/v1/segments?ratingKeys=2,1", `{"items":[{"analyzed":false,"ratingKey":"2","segments":[]},{"ratingKey":"1","segments":[{"end":61.7,"start":17.5,"type":"intro"},{"end":2987.9,"start":2909.1,"type":"credits"}]}]}`},
	}
	for _, c := range cases {
		code, body := get(t, srv.URL+c.path, "good")
		if code != 200 || compact(t, body) != c.want {
			t.Errorf("%s: %d %s\nwant %s", c.path, code, compact(t, body), c.want)
		}
	}
	if fa.calls != 1 {
		t.Errorf("token checked %d times, want 1 (cached)", fa.calls)
	}
}

func TestAuth(t *testing.T) {
	srv, _ := setup(t)
	if code, _ := get(t, srv.URL+"/v1/segments/1", ""); code != 401 {
		t.Errorf("no token: %d", code)
	}
	if code, _ := get(t, srv.URL+"/v1/segments/1", "bad"); code != 401 {
		t.Errorf("bad token: %d", code)
	}
	if code, _ := get(t, srv.URL+"/healthz", ""); code != 200 {
		t.Errorf("healthz: %d", code)
	}
	if code, _ := get(t, srv.URL+"/v1/segments", "good"); code != 400 {
		t.Errorf("empty batch: %d", code)
	}
	if code, _ := get(t, srv.URL+"/v1/preferences", "bad"); code != 401 {
		t.Errorf("preferences, bad token: %d", code)
	}
}

func TestCORS(t *testing.T) {
	srv, fa := setup(t)
	calls := fa.calls
	for _, p := range []string{"/v1/segments/1", "/v1/preferences/10", "/v1/preferences"} {
		req, _ := http.NewRequest(http.MethodOptions, srv.URL+p, nil)
		req.Header.Set("Origin", "http://example.test")
		req.Header.Set("Access-Control-Request-Method", "PUT")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		h := resp.Header
		if resp.StatusCode != 204 || h.Get("Access-Control-Allow-Origin") != "*" ||
			h.Get("Access-Control-Allow-Methods") != "GET, HEAD, PUT, OPTIONS" ||
			h.Get("Access-Control-Allow-Headers") != "X-Plex-Token, Content-Type, Accept, Range" ||
			h.Get("Access-Control-Expose-Headers") != "Content-Length, Content-Range, Accept-Ranges" ||
			h.Get("Access-Control-Max-Age") != "86400" {
			t.Errorf("preflight %s: %d %v", p, resp.StatusCode, h)
		}
	}
	if fa.calls != calls {
		t.Error("preflight checked a token")
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/segments/1", nil)
	req.Header.Set("X-Plex-Token", "bad")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("GET with bad token: %d, CORS %q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}
	resp, err = http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS on /healthz")
	}
}

func put(t *testing.T, url, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	req.Header.Set("X-Plex-Token", "good")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// dropTime removes updatedAt (checked to be present) for comparisons.
func dropTime(t *testing.T, s string) string {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad json %q: %v", s, err)
	}
	if _, ok := v["updatedAt"]; !ok && v["preferences"] != nil {
		t.Errorf("no updatedAt in %s", s)
	}
	delete(v, "updatedAt")
	b, _ := json.Marshal(v)
	return string(b)
}

func TestPreferences(t *testing.T) {
	srv, _ := setup(t)
	u := srv.URL + "/v1/preferences/"
	if code, body := get(t, u+"10", "good"); code != 200 || compact(t, body) != `{"key":"10","preferences":null}` {
		t.Fatalf("empty: %d %s", code, body)
	}
	obj := `{"audio":"rus", "shift":{"introStart":-2.5}}`
	want := `{"key":"10","preferences":{"audio":"rus","shift":{"introStart":-2.5}}}`
	if code, body := put(t, u+"10", obj); code != 200 || dropTime(t, body) != want {
		t.Fatalf("put: %d %s", code, body)
	}
	if code, body := get(t, u+"10", "good"); code != 200 || dropTime(t, body) != want {
		t.Fatalf("get: %d %s", code, body)
	}
	put(t, u+"7", `{"a":1}`)
	code, body := get(t, srv.URL+"/v1/preferences", "good")
	var list struct{ Items []map[string]any }
	if err := json.Unmarshal([]byte(body), &list); code != 200 || err != nil || len(list.Items) != 2 ||
		list.Items[0]["key"] != "10" || list.Items[1]["key"] != "7" {
		t.Fatalf("list: %d %s", code, body)
	}
	for _, bad := range []string{`[1]`, `"x"`, `{"a":`, `{"a":"` + strings.Repeat("x", 16<<10) + `"}`} {
		if code, _ := put(t, u+"10", bad); code != 400 {
			t.Errorf("put %.20q: %d, want 400", bad, code)
		}
	}
	for _, del := range []string{`{}`, `null`} {
		put(t, u+"7", `{"a":1}`)
		if code, body := put(t, u+"7", del); code != 200 || compact(t, body) != `{"key":"7","preferences":null}` {
			t.Errorf("delete with %s: %d %s", del, code, body)
		}
		if _, body := get(t, u+"7", "good"); compact(t, body) != `{"key":"7","preferences":null}` {
			t.Errorf("after delete with %s: %s", del, body)
		}
	}
	if _, body := get(t, u+"10", "good"); dropTime(t, body) != want {
		t.Errorf("bad puts changed the record: %s", body)
	}
}
