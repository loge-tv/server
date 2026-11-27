package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPickDBKeepsTheOldFileUntilThereIsANewOne(t *testing.T) {
	dir := t.TempDir()
	now, old := filepath.Join(dir, "sepia-server.db"), filepath.Join(dir, "detector.db")
	if got := pickDB(now, old); got != now {
		t.Fatalf("fresh install: %s", got)
	}
	os.WriteFile(old, nil, 0o600)
	if got := pickDB(now, old); got != old {
		t.Fatalf("old install: %s", got)
	}
	os.WriteFile(now, nil, 0o600)
	if got := pickDB(now, old); got != now {
		t.Fatalf("both files: %s", got)
	}
}
