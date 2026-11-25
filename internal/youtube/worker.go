package youtube

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed worker.py
var workerScript []byte

// errNoWorker: the worker can't run here (no python3, yt-dlp doesn't import):
// the resolver runs yt-dlp as a program instead.
var errNoWorker = errors.New("youtube: no yt-dlp worker")

// Worker keeps one python3 with yt-dlp loaded and asks it for one video at a
// time. Starting yt-dlp took ~2 s of the ~5.5 s a trailer's playlist took on a
// Raspberry Pi; loaded, the next ones take ~1.8 s.
type Worker struct {
	Python  string // python3
	Zipapp  string // the yt-dlp zipapp, imported from
	Dir     string // where worker.py is written
	Log     *slog.Logger
	Start   time.Duration // how long importing may take; 0: 60 s
	Retry   time.Duration // after a failed start, how long the program is used instead; 0: 10 min
	nowFunc func() time.Time

	mu       sync.Mutex
	cmd      *exec.Cmd
	in       io.WriteCloser
	out      *bufio.Reader
	exited   chan struct{}
	failedAt time.Time
}

func (w *Worker) now() time.Time {
	if w.nowFunc != nil {
		return w.nowFunc()
	}
	return time.Now()
}

// Warm starts the worker ahead of the first trailer.
func (w *Worker) Warm() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.startLocked()
}

// Restart stops the worker; the next request starts a new one (after yt-dlp
// updated itself).
func (w *Worker) Restart() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopLocked()
	w.failedAt = time.Time{}
}

// Info gives yt-dlp's answer for a video: {"formats": [...]} or {"error": "..."}.
// errNoWorker when the worker can't run; a request that outlives ctx kills
// the worker (it is stuck) and the next one starts it again.
func (w *Worker) Info(ctx context.Context, key string) ([]byte, error) {
	return w.ask(ctx, key)
}

// Search gives YouTube's first n videos for query, titles in lang (an
// uploader's translation when there is one): {"entries": [...]} or {"error": "..."}.
func (w *Worker) Search(ctx context.Context, query, lang string, n int) ([]byte, error) {
	line, err := json.Marshal(map[string]any{"search": query, "lang": lang, "n": n})
	if err != nil {
		return nil, err
	}
	return w.ask(ctx, string(line))
}

// ask sends one line (a video id, or a JSON request) and reads the answer.
func (w *Worker) ask(ctx context.Context, request string) ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.startLocked(); err != nil {
		return nil, err
	}
	if _, err := io.WriteString(w.in, request+"\n"); err != nil {
		w.stopLocked()
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	type reply struct {
		line []byte
		err  error
	}
	done := make(chan reply, 1)
	out := w.out
	go func() {
		line, err := out.ReadBytes('\n')
		done <- reply{line, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			w.stopLocked()
			return nil, fmt.Errorf("%w: worker: %v", ErrUnavailable, r.err)
		}
		return r.line, nil
	case <-ctx.Done():
		w.stopLocked()
		return nil, ctx.Err()
	}
}

func (w *Worker) startLocked() error {
	if w.cmd != nil {
		select {
		case <-w.exited:
			w.stopLocked()
		default:
			return nil
		}
	}
	retry := w.Retry
	if retry == 0 {
		retry = 10 * time.Minute
	}
	if !w.failedAt.IsZero() && w.now().Sub(w.failedAt) < retry {
		return errNoWorker
	}
	if err := w.spawnLocked(); err != nil {
		w.failedAt = w.now()
		if w.Log != nil {
			w.Log.Warn("yt-dlp worker", "err", err)
		}
		return fmt.Errorf("%w: %v", errNoWorker, err)
	}
	w.failedAt = time.Time{}
	return nil
}

func (w *Worker) spawnLocked() error {
	python := w.Python
	if python == "" {
		python = "python3"
	}
	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		return err
	}
	script := filepath.Join(w.Dir, "worker.py")
	if err := os.WriteFile(script, workerScript, 0o644); err != nil {
		return err
	}
	cmd := exec.Command(python, "-u", script, w.Zipapp)
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr tail
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	out := bufio.NewReaderSize(stdout, 64<<10)

	// The first line says yt-dlp imported: {"ready": version}.
	timeout := w.Start
	if timeout == 0 {
		timeout = time.Minute
	}
	ready := make(chan error, 1)
	go func() {
		line, err := out.ReadBytes('\n')
		if err != nil {
			ready <- fmt.Errorf("exited: %s", stderr.last())
			return
		}
		var hello struct {
			Ready string `json:"ready"`
		}
		if json.Unmarshal(line, &hello) != nil || hello.Ready == "" {
			ready <- fmt.Errorf("unexpected first line %q", strings.TrimSpace(string(line)))
			return
		}
		if w.Log != nil {
			w.Log.Info("yt-dlp worker", "version", hello.Ready)
		}
		ready <- nil
	}()
	var err2 error
	select {
	case err2 = <-ready:
	case <-time.After(timeout):
		err2 = errors.New("no answer while importing yt-dlp")
	}
	if err2 != nil {
		cmd.Process.Kill()
		<-exited
		return err2
	}
	w.cmd, w.in, w.out, w.exited = cmd, in, out, exited
	return nil
}

func (w *Worker) stopLocked() {
	if w.cmd == nil {
		return
	}
	w.in.Close()
	w.cmd.Process.Kill()
	<-w.exited
	w.cmd, w.in, w.out, w.exited = nil, nil, nil, nil
}

// tail keeps the last line written (python's traceback ends with the error).
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(p), nil
}

func (t *tail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	return lines[len(lines)-1]
}
