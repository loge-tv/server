package youtube

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const answer = `{"id":"AyIZ9tiiN8I","formats":[
 {"format_id":"233","protocol":"m3u8_native","manifest_url":"https://manifest.googlevideo.com/api/manifest/hls_variant/expire/1795442435/ei/x/ip/185.0.0.1/file/index.m3u8"},
 {"format_id":"18","protocol":"https"}]}`

func TestStreamFromHLSManifest(t *testing.T) {
	var runs atomic.Int32
	var args []string
	r := &Resolver{Bin: "yt-dlp", Run: func(_ context.Context, bin string, a ...string) ([]byte, error) {
		runs.Add(1)
		args = a
		return []byte(answer), nil
	}, Now: func() time.Time { return time.Unix(1795400000, 0) }}
	s, err := r.Stream(context.Background(), "AyIZ9tiiN8I")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.URL, "hls_variant") || !s.Expires.Equal(time.Unix(1795442435, 0)) {
		t.Fatalf("%+v", s)
	}
	if got := args[len(args)-1]; got != "https://www.youtube.com/watch?v=AyIZ9tiiN8I" {
		t.Fatalf("url %q", got)
	}
	if _, err := r.Stream(context.Background(), "AyIZ9tiiN8I"); err != nil || runs.Load() != 1 {
		t.Fatalf("cached: runs=%d err=%v", runs.Load(), err)
	}
}

func TestStreamExpiresAgain(t *testing.T) {
	now := time.Unix(1795400000, 0)
	var runs atomic.Int32
	r := &Resolver{Run: func(context.Context, string, ...string) ([]byte, error) {
		runs.Add(1)
		return []byte(answer), nil
	}, Now: func() time.Time { return now }}
	r.Stream(context.Background(), "AyIZ9tiiN8I")
	// 20 minutes before YouTube's expiry: too close, asked again.
	now = time.Unix(1795442435, 0).Add(-20 * time.Minute)
	r.Stream(context.Background(), "AyIZ9tiiN8I")
	if runs.Load() != 2 {
		t.Fatalf("runs=%d", runs.Load())
	}
}

func TestNoHLSAndFailures(t *testing.T) {
	r := &Resolver{Run: func(_ context.Context, _ string, a ...string) ([]byte, error) {
		if strings.HasSuffix(a[len(a)-1], "broken") {
			return nil, errors.New("exit status 1: ERROR: Sign in to confirm")
		}
		return []byte(`{"formats":[{"protocol":"https","url":"x"}]}`), nil
	}}
	if _, err := r.Stream(context.Background(), "nohls"); !errors.Is(err, ErrNoStream) {
		t.Fatalf("no hls: %v", err)
	}
	if _, err := r.Stream(context.Background(), "broken"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failure: %v", err)
	}
}

func TestConcurrentCallersShareOneRun(t *testing.T) {
	var runs atomic.Int32
	release := make(chan struct{})
	r := &Resolver{Run: func(context.Context, string, ...string) ([]byte, error) {
		runs.Add(1)
		<-release
		return []byte(answer), nil
	}}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Stream(context.Background(), "AyIZ9tiiN8I"); err != nil {
				t.Error(err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if runs.Load() != 1 {
		t.Fatalf("runs=%d", runs.Load())
	}
}

func TestCallerGivingUpDoesNotCancelTheRun(t *testing.T) {
	release := make(chan struct{})
	var runs atomic.Int32
	r := &Resolver{Run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		runs.Add(1)
		select {
		case <-release:
			return []byte(answer), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := r.Stream(ctx, "AyIZ9tiiN8I"); !errors.Is(err, context.Canceled) {
		t.Fatalf("first caller: %v", err)
	}
	close(release)
	if _, err := r.Stream(context.Background(), "AyIZ9tiiN8I"); err != nil || runs.Load() != 1 {
		t.Fatalf("next caller gets that run: runs=%d err=%v", runs.Load(), err)
	}
}

func TestUpdatableFallsBack(t *testing.T) {
	if got := Updatable("no-such-yt-dlp", t.TempDir()); got != "no-such-yt-dlp" {
		t.Fatalf("missing binary: %q", got)
	}
	// A copy that doesn't run (here: not yt-dlp, exits non-zero) leaves the original.
	if got := Updatable("false", t.TempDir()); !strings.HasSuffix(got, "/false") || strings.HasPrefix(got, os.TempDir()) {
		t.Fatalf("copy that fails: %q", got)
	}
}
