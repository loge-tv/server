// Package youtube turns a YouTube video into a stream a TV plays itself: tvOS
// has no web view to embed YouTube's player in. yt-dlp finds the video's HLS
// master playlist; YouTube binds it to the IP that asked, so it plays only from
// the network the detector is in — at home.
package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Stream is an HLS master playlist of a video (video and audio together).
type Stream struct {
	URL     string    `json:"url"`
	Expires time.Time `json:"expiresAt"`
}

var (
	// ErrNoStream: yt-dlp answered, but the video has no HLS playlist.
	ErrNoStream = errors.New("youtube: no hls stream")
	// ErrUnavailable: yt-dlp failed (YouTube refused, unreachable, or yt-dlp too old).
	ErrUnavailable = errors.New("youtube: yt-dlp failed")
)

// Runner runs yt-dlp and returns its stdout.
type Runner func(ctx context.Context, bin string, args ...string) ([]byte, error)

// Resolver asks yt-dlp, remembers each answer until shortly before YouTube
// lets the playlist expire and runs one yt-dlp at a time (a Raspberry Pi that
// also decodes audio for the analysis).
type Resolver struct {
	Bin     string
	Timeout time.Duration
	Log     *slog.Logger
	Run     Runner           // nil: exec
	Now     func() time.Time // nil: time.Now

	once    sync.Once
	mu      sync.Mutex
	cache   map[string]Stream
	flights map[string]*flight
	slots   chan struct{}
}

type flight struct {
	done   chan struct{}
	stream Stream
	err    error
}

// The playlist is good for hours; a TV asking just before it expires would
// get one that dies mid-trailer.
const expiryMargin = 30 * time.Minute

// Without the expiry in the URL: what YouTube usually gives (6 h), minus the margin.
const defaultLifetime = 5 * time.Hour

var expireRe = regexp.MustCompile(`/expire/(\d+)/`)

func (r *Resolver) init() {
	r.once.Do(func() {
		r.cache = map[string]Stream{}
		r.flights = map[string]*flight{}
		r.slots = make(chan struct{}, 1)
		if r.Run == nil {
			r.Run = execRun
		}
		if r.Now == nil {
			r.Now = time.Now
		}
		if r.Log == nil {
			r.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
		}
		if r.Timeout == 0 {
			r.Timeout = 45 * time.Second
		}
	})
}

// Stream gives the HLS playlist of the video with this YouTube id. Callers
// asking for the same video while yt-dlp runs wait for that run.
func (r *Resolver) Stream(ctx context.Context, key string) (Stream, error) {
	r.init()
	r.mu.Lock()
	if s, ok := r.cache[key]; ok && r.Now().Before(s.Expires.Add(-expiryMargin)) {
		r.mu.Unlock()
		return s, nil
	}
	f, running := r.flights[key]
	if !running {
		f = &flight{done: make(chan struct{})}
		r.flights[key] = f
	}
	r.mu.Unlock()

	if !running {
		// Not tied to the first caller: a TV that gives up doesn't waste the run for the next one.
		go r.resolve(key, f)
	}
	select {
	case <-f.done:
		return f.stream, f.err
	case <-ctx.Done():
		return Stream{}, ctx.Err()
	}
}

func (r *Resolver) resolve(key string, f *flight) {
	defer func() {
		r.mu.Lock()
		delete(r.flights, key)
		if f.err == nil {
			r.cache[key] = f.stream
		}
		r.mu.Unlock()
		close(f.done)
	}()
	r.slots <- struct{}{}
	defer func() { <-r.slots }()

	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()
	started := r.Now()
	out, err := r.Run(ctx, r.Bin, "--no-config", "--no-warnings", "--no-playlist", "--skip-download",
		"--dump-json", "https://www.youtube.com/watch?v="+key)
	if err != nil {
		r.Log.Warn("yt-dlp", "key", key, "err", err)
		f.err = fmt.Errorf("%w: %v", ErrUnavailable, err)
		return
	}
	f.stream, f.err = parse(out, r.Now())
	r.Log.Info("trailer stream", "key", key, "ok", f.err == nil, "took", r.Now().Sub(started).Round(time.Millisecond))
}

func parse(out []byte, now time.Time) (Stream, error) {
	var info struct {
		Formats []struct {
			ManifestURL string `json:"manifest_url"`
			Protocol    string `json:"protocol"`
		} `json:"formats"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return Stream{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	for _, f := range info.Formats {
		if f.ManifestURL == "" || !strings.HasPrefix(f.Protocol, "m3u8") {
			continue
		}
		expires := now.Add(defaultLifetime + expiryMargin)
		if m := expireRe.FindStringSubmatch(f.ManifestURL); m != nil {
			if unix, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				expires = time.Unix(unix, 0)
			}
		}
		return Stream{URL: f.ManifestURL, Expires: expires.UTC()}, nil
	}
	return Stream{}, ErrNoStream
}

func execRun(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		return nil, fmt.Errorf("%v: %s", err, msg)
	}
	return out, nil
}

// Updatable copies yt-dlp to dir, where it can replace itself (the image's
// copy is read-only), and returns the copy's path; bin as is when the copy
// fails or doesn't run (a noexec /tmp).
func Updatable(bin, dir string) string {
	src, err := exec.LookPath(bin)
	if err != nil {
		return bin
	}
	dst := filepath.Join(dir, "yt-dlp")
	if err := copyFile(src, dst); err != nil {
		return src
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, dst, "--version").Run(); err != nil {
		return src
	}
	return dst
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// UpdateLoop runs `yt-dlp -U` now and then every interval: YouTube changes
// break old yt-dlp releases within weeks.
func (r *Resolver) UpdateLoop(ctx context.Context, interval time.Duration) {
	r.init()
	for {
		uctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		out, err := r.Run(uctx, r.Bin, "--no-config", "-U")
		cancel()
		if err != nil {
			r.Log.Warn("yt-dlp update", "err", err)
		} else {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			r.Log.Info("yt-dlp update", "result", lines[len(lines)-1])
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
