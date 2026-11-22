package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/loge-tv/detector/internal/media"
)

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
	if err := st.PutBlackScan(ctx, "1", 810, []media.Frame{{T: 1250, PBlack: 99}}); err != nil {
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
	if _, start, ok, err := st.BlackScan(ctx, "1"); err != nil || !ok || start != 810 {
		t.Fatalf("black scan: start %v ok %v err %v", start, ok, err)
	}
}
