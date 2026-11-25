package api

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sepiatv/server/internal/plex"
)

// fakeFiles: item 1 has part 11 (a file on disk) and part 12 (gone from
// disk); "limited" may not see item 1 (a shared user without that library).
type fakeFiles struct {
	dir   string
	calls int
}

func (f *fakeFiles) PartFile(_ context.Context, token, key string, part int64) (string, error) {
	f.calls++
	switch {
	case key != "1":
		return "", &plex.StatusError{Code: http.StatusNotFound, Path: "/library/metadata/" + key}
	case token != "good":
		return "", &plex.StatusError{Code: http.StatusForbidden, Path: "/library/metadata/1"}
	case part == 11:
		return filepath.Join(f.dir, "Show S01E01.mkv"), nil
	case part == 12:
		return filepath.Join(f.dir, "gone.mp4"), nil
	}
	return "", plex.ErrNoPart
}

func do(t *testing.T, method, url string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestFiles(t *testing.T) {
	ff := &fakeFiles{dir: t.TempDir()}
	content := "0123456789abcdef"
	if err := os.WriteFile(filepath.Join(ff.dir, "Show S01E01.mkv"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, _ := setupWith(t, ff)
	u := srv.URL + "/v1/files/1/11"
	good := map[string]string{"X-Plex-Token": "good"}

	resp, body := do(t, http.MethodGet, u, good)
	if resp.StatusCode != 200 || body != content || resp.Header.Get("Content-Type") != "video/x-matroska" ||
		resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("full: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	resp, body = do(t, http.MethodGet, u+"/file.mkv", map[string]string{"X-Plex-Token": "good", "Range": "bytes=2-5"})
	if resp.StatusCode != 206 || body != "2345" || resp.Header.Get("Content-Range") != "bytes 2-5/16" {
		t.Errorf("range: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	resp, body = do(t, http.MethodHead, u, good)
	if resp.StatusCode != 200 || body != "" || resp.ContentLength != 16 {
		t.Errorf("head: %d %q len %d", resp.StatusCode, body, resp.ContentLength)
	}
	if ff.calls != 1 {
		t.Errorf("plex asked %d times, want 1 (cached)", ff.calls)
	}
	// <video src> can't send headers: the token may come in the query, for files only.
	if resp, _ := do(t, http.MethodGet, u+"?X-Plex-Token=good", nil); resp.StatusCode != 200 {
		t.Errorf("query token: %d", resp.StatusCode)
	}
	if resp, _ := do(t, http.MethodGet, srv.URL+"/v1/segments/1?X-Plex-Token=good", nil); resp.StatusCode != 401 {
		t.Errorf("query token on segments: %d, want 401", resp.StatusCode)
	}

	cases := []struct {
		path, token string
		want        int
	}{
		{"/v1/files/1/11", "", 401},
		{"/v1/files/1/11", "bad", 401},
		{"/v1/files/1/11", "limited", 403},
		{"/v1/files/2/11", "good", 404},
		{"/v1/files/1/13", "good", 404},
		{"/v1/files/1/12", "good", 503},
		{"/v1/files/1/x", "good", 400},
	}
	for _, c := range cases {
		hdr := map[string]string{}
		if c.token != "" {
			hdr["X-Plex-Token"] = c.token
		}
		if resp, body := do(t, http.MethodGet, srv.URL+c.path, hdr); resp.StatusCode != c.want {
			t.Errorf("%s as %q: %d %s, want %d", c.path, c.token, resp.StatusCode, body, c.want)
		}
	}
}

func TestFilesOff(t *testing.T) {
	srv, _ := setup(t)
	if code, _ := get(t, srv.URL+"/v1/files/1/11", "good"); code != 404 {
		t.Errorf("no FileFinder: %d, want 404", code)
	}
}
