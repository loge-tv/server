package youtube

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A yt_dlp stand-in for worker.py: imports are counted in $FAKE_YTDLP_IMPORTS,
// and a few ids misbehave.
const fakeYtDlp = `
import os, time
from . import version
with open(os.environ["FAKE_YTDLP_IMPORTS"], "a") as f:
    f.write("import\n")

class YoutubeDL:
    def __init__(self, opts):
        assert opts["skip_download"]
    def extract_info(self, url, download, process):
        assert not download and not process
        key = url.rsplit("=", 1)[1]
        if key == "broken":
            raise Exception("ERROR: [youtube] broken: Sign in to confirm you're not a bot\n")
        if key == "dies":
            os._exit(3)
        if key == "hangs":
            time.sleep(60)
        return {"id": key, "formats": [
            {"format_id": "233", "protocol": "m3u8_native",
             "manifest_url": "https://manifest.googlevideo.com/api/manifest/hls_variant/expire/1795442435/id/" + key + "/index.m3u8"},
            {"format_id": "18", "protocol": "https", "url": "https://x"},
        ]}
`

func fakeWorker(t *testing.T) (*Worker, func() int) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	lib := t.TempDir()
	pkg := filepath.Join(lib, "yt_dlp")
	os.MkdirAll(pkg, 0o755)
	os.WriteFile(filepath.Join(pkg, "__init__.py"), []byte(fakeYtDlp), 0o644)
	os.WriteFile(filepath.Join(pkg, "version.py"), []byte(`__version__ = "2026.10.26"`+"\n"), 0o644)
	imports := filepath.Join(t.TempDir(), "imports")
	t.Setenv("FAKE_YTDLP_IMPORTS", imports)
	w := &Worker{Python: python, Zipapp: lib, Dir: t.TempDir()}
	t.Cleanup(w.Restart)
	return w, func() int {
		b, _ := os.ReadFile(imports)
		return strings.Count(string(b), "import")
	}
}

func TestWorkerAnswersFromOneProcess(t *testing.T) {
	w, imports := fakeWorker(t)
	r := &Resolver{Worker: w, Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("ran yt-dlp as a program")
		return nil, nil
	}, Now: func() time.Time { return time.Unix(1795400000, 0) }}
	for _, key := range []string{"AyIZ9tiiN8I", "dQw4w9WgXcQ", "broken", "f2oHMze7RwY"} {
		s, err := r.Stream(context.Background(), key)
		if key == "broken" {
			// yt-dlp's error is an answer: the same process goes on with the next video.
			if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "Sign in") {
				t.Fatalf("broken: %v", err)
			}
			continue
		}
		if err != nil || !strings.Contains(s.URL, "/id/"+key+"/") || !s.Expires.Equal(time.Unix(1795442435, 0)) {
			t.Fatalf("%s: %+v %v", key, s, err)
		}
	}
	if n := imports(); n != 1 {
		t.Fatalf("yt-dlp loaded %d times", n)
	}
}

func TestWorkerStartsAgainAfterDyingOrHanging(t *testing.T) {
	w, imports := fakeWorker(t)
	if _, err := w.Info(context.Background(), "dies"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("dies: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := w.Info(ctx, "hangs"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hangs: %v", err)
	}
	out, err := w.Info(context.Background(), "AyIZ9tiiN8I")
	if err != nil || !strings.Contains(string(out), "hls_variant") {
		t.Fatalf("after: %s %v", out, err)
	}
	if n := imports(); n != 3 {
		t.Fatalf("loaded %d times, want 3 (first, after dying, after hanging)", n)
	}
}

func TestWithoutWorkerTheProgramRuns(t *testing.T) {
	var runs int
	r := &Resolver{Worker: &Worker{Python: "/no/such/python3", Dir: t.TempDir()},
		Run: func(context.Context, string, ...string) ([]byte, error) {
			runs++
			return []byte(answer), nil
		}}
	for _, key := range []string{"AyIZ9tiiN8I", "dQw4w9WgXcQ"} {
		if _, err := r.Stream(context.Background(), key); err != nil {
			t.Fatal(err)
		}
	}
	if runs != 2 {
		t.Fatalf("runs=%d", runs)
	}
	// A failed start isn't retried on every video.
	if !r.Worker.failedAt.After(time.Time{}) || r.Worker.Warm() == nil {
		t.Fatal("tried to start again at once")
	}
}

func TestWorkerAgainstRealYtDlp(t *testing.T) {
	zipapp := os.Getenv("YTDLP_ZIPAPP") // e.g. a downloaded yt-dlp release; asks YouTube
	if zipapp == "" {
		t.Skip("YTDLP_ZIPAPP not set")
	}
	w := &Worker{Python: "python3", Zipapp: zipapp, Dir: t.TempDir()}
	defer w.Restart()
	r := &Resolver{Worker: w}
	for range 2 {
		started := time.Now()
		s, err := r.Stream(context.Background(), "dQw4w9WgXcQ")
		if err != nil || !strings.Contains(s.URL, "googlevideo.com") {
			t.Fatalf("%v", err)
		}
		t.Logf("took %s", time.Since(started).Round(time.Millisecond))
		r.cache = map[string]Stream{}
	}
}
