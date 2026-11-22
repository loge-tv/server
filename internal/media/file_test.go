package media

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStat(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.mkv")
	if err := os.WriteFile(p, make([]byte, 10000), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Stat(p)
	if err != nil || s.Size != 10000 || s.MTime == 0 {
		t.Fatalf("got %+v %v", s, err)
	}
	// Rewritten with the same size: the state changes.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	if s2, _ := Stat(p); s2 == s {
		t.Fatal("modification time not in the state")
	}
	// A disk that dropped out: the folder is gone.
	if _, err := Stat(filepath.Join(dir, "gone", "a.mkv")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing file: %v", err)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Stat(p); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("empty file: %v", err)
	}
}
