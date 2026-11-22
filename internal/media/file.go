package media

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrUnavailable: the file Plex lists can't be read right now (the disk
// dropped out, a remount): a temporary condition, not a broken file.
var ErrUnavailable = errors.New("file unavailable")

// FileState identifies the content of a file for caches: a replaced or
// rewritten file changes size or modification time.
type FileState struct {
	Size  int64 `json:"size"`
	MTime int64 `json:"mtime"` // unix nanoseconds
}

// Stat checks that path can be read (its first and last bytes) and returns
// its state. Failures wrap ErrUnavailable.
func Stat(path string) (FileState, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileState{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return FileState{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if fi.Size() == 0 {
		return FileState{}, fmt.Errorf("%w: %s is empty", ErrUnavailable, path)
	}
	buf := make([]byte, 4096)
	for _, off := range []int64{0, max(0, fi.Size()-int64(len(buf)))} {
		if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
			return FileState{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	}
	return FileState{Size: fi.Size(), MTime: fi.ModTime().UnixNano()}, nil
}
