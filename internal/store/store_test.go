package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/loge-tv/detector/internal/media"
)

// Attempts exhausted by one analyzer version are retried by the next one,
// once: its own failures stay counted.
func TestResetAttempts(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.UpsertItem(ctx, Item{RatingKey: "1", Kind: "movie", Path: "/x", Size: 1, Duration: 100}); err != nil {
		t.Fatal(err)
	}
	attempts := func() int {
		t.Helper()
		it, _, err := st.Item(ctx, "1")
		if err != nil {
			t.Fatal(err)
		}
		return it.Attempts
	}
	for range 3 {
		if err := st.Fail(ctx, "1", 7, errors.New("timeout")); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := st.ResetAttempts(ctx, 7); n != 0 || attempts() != 3 {
		t.Fatalf("same version reset: %d items, attempts %d", n, attempts())
	}
	if n, _ := st.ResetAttempts(ctx, 8); n != 1 || attempts() != 0 {
		t.Fatalf("new version: %d items, attempts %d", n, attempts())
	}
	if err := st.Fail(ctx, "1", 8, errors.New("timeout")); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.ResetAttempts(ctx, 8); n != 0 || attempts() != 1 {
		t.Fatalf("own failure reset: %d items, attempts %d", n, attempts())
	}
}

// Plex first reports whole minutes, then the real duration: growth re-opens
// analysis but keeps the caches (the analyzer decides what is stale).
func TestUpsertDurationGrowth(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	it := Item{RatingKey: "1", Kind: "episode", Path: "/x", Size: 1, Duration: 1260}
	if err := st.UpsertItem(ctx, it); err != nil {
		t.Fatal(err)
	}
	if err := st.PutLumaCheck(ctx, "1", 1250, []media.Luma{{Low: 0}}); err != nil {
		t.Fatal(err)
	}
	file := media.FileState{Size: 1, MTime: 42}
	if err := st.PutEndScan(ctx, "1", EndScan{Start: 810, File: file, Frames: []media.Frame{{T: 1250, PBlack: 99}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetResult(ctx, "1", 2, nil); err != nil {
		t.Fatal(err)
	}
	check := func(dur float64, analyzed bool) {
		t.Helper()
		it.Duration = dur
		if err := st.UpsertItem(ctx, it); err != nil {
			t.Fatal(err)
		}
		got, _, err := st.Item(ctx, "1")
		if err != nil {
			t.Fatal(err)
		}
		if got.Duration != dur || got.Analyzed != analyzed || got.Algo != 2 {
			t.Fatalf("duration %v: got %+v, want analyzed=%v", dur, got, analyzed)
		}
	}
	check(1258, true)  // shrank
	check(1262, true)  // wobble
	check(1284, false) // grew
	if sc, ok, err := st.EndScan(ctx, "1"); err != nil || !ok || sc.Start != 810 || sc.File != file || len(sc.Frames) != 1 {
		t.Fatalf("end scan: %+v ok %v err %v", sc, ok, err)
	}
	// A new scan drops the luma probes of the old one.
	if _, ok, _ := st.LumaCheck(ctx, "1", 1250); ok {
		t.Fatal("luma probe of the previous scan kept")
	}
}
