package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/emildeev/skiptv/detector/internal/store"
)

type fakeAuth struct{ calls int }

func (f *fakeAuth) CheckAccess(_ context.Context, token string) (bool, error) {
	f.calls++
	return token == "good", nil
}

func setup(t *testing.T) (*httptest.Server, *fakeAuth) {
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
	if err := st.SetResult(ctx, "1", []store.Segment{
		{Type: "credits", Start: 2909.1, End: 2987.9, Source: "fingerprint"},
		{Type: "intro", Start: 17.5, End: 61.7, Source: "fingerprint"},
	}); err != nil {
		t.Fatal(err)
	}
	fa := &fakeAuth{}
	srv := httptest.NewServer((&Server{Store: st, Auth: fa, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
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
}
