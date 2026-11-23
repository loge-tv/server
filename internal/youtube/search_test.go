package youtube

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSearchThroughWorker(t *testing.T) {
	w, imports := fakeWorker(t)
	r := &Resolver{Worker: w, Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("ran yt-dlp as a program")
		return nil, nil
	}}
	found, err := r.Search(context.Background(), "  Зеленее   травы трейлер ", "ru", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 3 || found[0].ID != "v0" || found[0].Title != "Зеленее травы трейлер #0 (ru)" || found[2].Duration != 62 {
		t.Fatalf("%+v", found)
	}
	// Videos and searches share the one process.
	if _, err := r.Stream(context.Background(), "AyIZ9tiiN8I"); err != nil {
		t.Fatal(err)
	}
	if imports() != 1 {
		t.Fatalf("imports %d", imports())
	}
}

func TestSearchAsProgramAndKept(t *testing.T) {
	var runs atomic.Int32
	var args []string
	now := time.Unix(1795400000, 0)
	r := &Resolver{Bin: "yt-dlp", Run: func(_ context.Context, _ string, a ...string) ([]byte, error) {
		runs.Add(1)
		args = a
		return []byte(`{"_type":"playlist","entries":[{"id":"IP5B4Z9oCLc","title":"Зеленее травы — Русский трейлер (2019)","duration":137.0,"channel":"iVideos"},{"id":"","title":"x"}]}`), nil
	}, Now: func() time.Time { return now }}
	found, err := r.Search(context.Background(), "Зеленее травы 2019 трейлер", "ru", 10)
	if err != nil || len(found) != 1 || found[0].ID != "IP5B4Z9oCLc" || found[0].Channel != "iVideos" {
		t.Fatalf("%+v %v", found, err)
	}
	got := strings.Join(args, " ")
	if !strings.Contains(got, "--flat-playlist") || !strings.Contains(got, "youtube:lang=ru") ||
		!strings.HasSuffix(got, "ytsearch10:Зеленее травы 2019 трейлер") {
		t.Fatalf("args %q", got)
	}
	r.Search(context.Background(), "Зеленее травы 2019 трейлер", "ru", 10)
	if runs.Load() != 1 {
		t.Fatalf("kept: runs=%d", runs.Load())
	}
	now = now.Add(25 * time.Hour)
	r.Search(context.Background(), "Зеленее травы 2019 трейлер", "ru", 10)
	if runs.Load() != 2 {
		t.Fatalf("a day later: runs=%d", runs.Load())
	}
}

func TestSearchFailureIsNotKept(t *testing.T) {
	var runs atomic.Int32
	r := &Resolver{Run: func(context.Context, string, ...string) ([]byte, error) {
		runs.Add(1)
		return []byte(`{"error":"ERROR: Sign in to confirm"}`), nil
	}}
	for range 2 {
		if _, err := r.Search(context.Background(), "q", "ru", 10); err == nil {
			t.Fatal("no error")
		}
	}
	if runs.Load() != 2 {
		t.Fatalf("runs=%d", runs.Load())
	}
}

// A press goes ahead of a page's search waiting for yt-dlp.
func TestPressGoesAheadOfSearch(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var order []string
	r := &Resolver{Run: func(_ context.Context, _ string, a ...string) ([]byte, error) {
		last := a[len(a)-1]
		mu.Lock()
		order = append(order, last)
		mu.Unlock()
		if strings.HasSuffix(last, "first") {
			<-release
		}
		if strings.HasPrefix(last, "ytsearch") {
			return []byte(`{"entries":[]}`), nil
		}
		return []byte(answer), nil
	}, Now: func() time.Time { return time.Unix(1795400000, 0) }}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); r.Prefetch(context.Background(), "first") }()
	time.Sleep(50 * time.Millisecond)
	go func() { defer wg.Done(); r.Search(context.Background(), "page", "ru", 10) }()
	time.Sleep(50 * time.Millisecond)
	go func() { defer wg.Done(); r.Stream(context.Background(), "pressed") }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if len(order) != 3 || !strings.HasSuffix(order[1], "pressed") || !strings.HasSuffix(order[2], "ytsearch10:page") {
		t.Fatalf("order %q", order)
	}
}

// A page that left before its search began drops it.
func TestSearchDroppedWhenPageLeft(t *testing.T) {
	release := make(chan struct{})
	var searched atomic.Int32
	r := &Resolver{Run: func(_ context.Context, _ string, a ...string) ([]byte, error) {
		if strings.HasPrefix(a[len(a)-1], "ytsearch") {
			searched.Add(1)
			return []byte(`{"entries":[]}`), nil
		}
		<-release
		return []byte(answer), nil
	}, Now: func() time.Time { return time.Unix(1795400000, 0) }}
	go r.Stream(context.Background(), "busy")
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := r.Search(ctx, "gone", "ru", 10); err == nil {
		t.Fatal("waited past the page")
	}
	close(release)
	time.Sleep(100 * time.Millisecond)
	if searched.Load() != 0 {
		t.Fatal("searched for a page that left")
	}
}
