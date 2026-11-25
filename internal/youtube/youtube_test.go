package youtube

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

// orderedRuns blocks every yt-dlp run until released and records the order.
type orderedRuns struct {
	mu      sync.Mutex
	order   []string
	release chan struct{}
}

func (o *orderedRuns) run(_ context.Context, _ string, a ...string) ([]byte, error) {
	o.mu.Lock()
	o.order = append(o.order, strings.TrimPrefix(a[len(a)-1], "https://www.youtube.com/watch?v="))
	o.mu.Unlock()
	<-o.release
	return []byte(answer), nil
}

func (o *orderedRuns) got() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return strings.Join(o.order, " ")
}

func waitQueued(t *testing.T, r *Resolver, n int) {
	t.Helper()
	for range 200 {
		r.mu.Lock()
		q := len(r.queue)
		r.mu.Unlock()
		if q == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("queue never reached %d", n)
}

func TestPressGoesAheadOfPrefetches(t *testing.T) {
	o := &orderedRuns{release: make(chan struct{})}
	r := &Resolver{Run: o.run}
	var wg sync.WaitGroup
	ask := func(key string, press bool) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if press {
				_, err = r.Stream(context.Background(), key)
			} else {
				_, err = r.Prefetch(context.Background(), key)
			}
			if err != nil {
				t.Error(key, err)
			}
		}()
	}
	ask("pageA00", false) // runs at once
	waitQueued(t, r, 0)
	for o.got() == "" {
		time.Sleep(time.Millisecond)
	}
	ask("pageB00", false)
	waitQueued(t, r, 1)
	ask("pageC00", false)
	waitQueued(t, r, 2)
	ask("press00", true)
	waitQueued(t, r, 3)
	ask("pageC00", true) // play pressed on a page whose prefetch is waiting
	time.Sleep(20 * time.Millisecond)
	close(o.release)
	wg.Wait()
	if got := o.got(); got != "pageA00 pageC00 press00 pageB00" {
		t.Fatalf("order %q", got)
	}
}

func TestLeftPageDropsItsWaitingPrefetch(t *testing.T) {
	o := &orderedRuns{release: make(chan struct{})}
	r := &Resolver{Run: o.run}
	done := make(chan struct{})
	go func() { r.Stream(context.Background(), "playing"); close(done) }()
	for o.got() == "" {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	left := make(chan error)
	go func() { _, err := r.Prefetch(ctx, "leftpage"); left <- err }()
	waitQueued(t, r, 1)
	cancel()
	if err := <-left; !errors.Is(err, context.Canceled) {
		t.Fatalf("left: %v", err)
	}
	close(o.release)
	<-done
	time.Sleep(20 * time.Millisecond)
	if got := o.got(); got != "playing" {
		t.Fatalf("runs %q", got)
	}
	// Asked again later: a new run, not a dropped one.
	if _, err := r.Prefetch(context.Background(), "leftpage"); err != nil || o.got() != "playing leftpage" {
		t.Fatalf("again: %v %q", err, o.got())
	}
}

// updateRuns stands in for yt-dlp: -U writes the new release into the file it
// is run as; a video records which release it read and waits to be released.
type updateRuns struct {
	mu      sync.Mutex
	seen    []string
	updated chan string // the file -U ran as
	release chan struct{}
	result  string // -U's last line; "up to date" leaves the file as it was
}

func (u *updateRuns) run(_ context.Context, bin string, a ...string) ([]byte, error) {
	if a[len(a)-1] == "-U" {
		if !strings.Contains(u.result, "up to date") {
			os.WriteFile(bin, []byte("2026.11.23"), 0o755)
		}
		u.updated <- bin
		return []byte("Updating…\n" + u.result + "\n"), nil
	}
	b, _ := os.ReadFile(bin)
	u.mu.Lock()
	u.seen = append(u.seen, string(b))
	u.mu.Unlock()
	<-u.release
	return []byte(answer), nil
}

func (u *updateRuns) got() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return strings.Join(u.seen, " ")
}

func TestUpdateReplacesYtDlpBetweenRuns(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "yt-dlp")
	os.WriteFile(bin, []byte("2026.10.26"), 0o755)
	u := &updateRuns{updated: make(chan string, 1), release: make(chan struct{}), result: "Updated yt-dlp to stable@2026.11.23"}
	r := &Resolver{Bin: bin, Run: u.run}
	first := make(chan error)
	go func() { _, err := r.Stream(context.Background(), "playing0000"); first <- err }()
	for u.got() == "" {
		time.Sleep(time.Millisecond)
	}
	updated := make(chan struct{})
	go func() { r.update(context.Background()); close(updated) }()

	// The download runs on a copy while the trailer plays on with the file it reads.
	if got := <-u.updated; got == bin {
		t.Fatal("yt-dlp -U ran on the file in use")
	}
	waitQueued(t, r, 1) // the swap, waiting for the trailer
	second := make(chan error)
	go func() { _, err := r.Stream(context.Background(), "pressed0000"); second <- err }()
	waitQueued(t, r, 2)
	if b, _ := os.ReadFile(bin); string(b) != "2026.10.26" {
		t.Fatalf("replaced under a running yt-dlp: %s", b)
	}
	close(u.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	<-updated
	if got := u.got(); got != "2026.10.26 2026.11.23" {
		t.Fatalf("releases read: %q", got)
	}
	if _, err := os.Stat(bin + ".next"); !os.IsNotExist(err) {
		t.Fatalf("copy left: %v", err)
	}
}

func TestUpdateUpToDateOrFailedKeepsYtDlp(t *testing.T) {
	for _, result := range []string{"yt-dlp is up to date (stable@2026.11.23)", "fails"} {
		bin := filepath.Join(t.TempDir(), "yt-dlp")
		os.WriteFile(bin, []byte("2026.11.23"), 0o755)
		u := &updateRuns{updated: make(chan string, 1), release: make(chan struct{}), result: result}
		r := &Resolver{Bin: bin, Run: u.run}
		if result == "fails" {
			r.Run = func(context.Context, string, ...string) ([]byte, error) {
				return nil, errors.New("exit status 1: ERROR: Unable to download")
			}
		}
		r.update(context.Background())
		if b, _ := os.ReadFile(bin); string(b) != "2026.11.23" {
			t.Fatalf("%s: file now %q", result, b)
		}
		if _, err := os.Stat(bin + ".next"); !os.IsNotExist(err) {
			t.Fatalf("%s: copy left: %v", result, err)
		}
		r.mu.Lock()
		q, busy := len(r.queue), r.busy
		r.mu.Unlock()
		if q != 0 || busy {
			t.Fatalf("%s: queue %d busy %v", result, q, busy)
		}
	}
}
