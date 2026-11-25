// Package youtube turns a YouTube video into a stream a TV plays itself: tvOS
// has no web view to embed YouTube's player in. yt-dlp finds the video's HLS
// master playlist; YouTube binds it to the IP that asked, so it plays only from
// the network the server is in — at home.
package youtube

import (
	"bytes"
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
// also decodes audio for the analysis). A video someone pressed play on goes
// ahead of the ones pages ask for in advance.
type Resolver struct {
	Bin     string
	Worker  *Worker // nil, or it can't run: yt-dlp runs as a program for every video
	Timeout time.Duration
	Log     *slog.Logger
	Run     Runner           // nil: exec
	Now     func() time.Time // nil: time.Now

	once     sync.Once
	mu       sync.Mutex
	cache    map[string]Stream
	searches map[string]searchEntry
	flights  map[string]*flight
	queue    []*flight // waiting for yt-dlp, in the order asked
	busy     bool      // yt-dlp is on one
}

type flight struct {
	key     string
	asked   time.Time
	done    chan struct{}
	stream  Stream
	err     error
	callers int  // waiting for it
	urgent  bool // someone pressed play, not only a page asking in advance
	started bool

	search  *searchJob // a search, not a video's stream
	results []Result

	swap func() error // not yt-dlp: a new release takes the place of the current one
}

type searchEntry struct {
	results []Result
	expires time.Time
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
		r.searches = map[string]searchEntry{}
		r.flights = map[string]*flight{}
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

// Stream gives the HLS playlist of the video with this YouTube id: someone
// pressed play. Callers asking for the same video while yt-dlp runs wait for
// that run.
func (r *Resolver) Stream(ctx context.Context, key string) (Stream, error) {
	return r.get(ctx, key, true)
}

// Prefetch is Stream for a page that opened and may play its trailer: it
// waits behind the videos someone pressed play on, and is dropped when every
// page asking for it left before yt-dlp got to it.
func (r *Resolver) Prefetch(ctx context.Context, key string) (Stream, error) {
	return r.get(ctx, key, false)
}

func (r *Resolver) get(ctx context.Context, key string, urgent bool) (Stream, error) {
	r.init()
	r.mu.Lock()
	if s, ok := r.cache[key]; ok && r.Now().Before(s.Expires.Add(-expiryMargin)) {
		r.mu.Unlock()
		return s, nil
	}
	f, ok := r.flights[key]
	if !ok {
		f = &flight{key: key, asked: r.Now(), done: make(chan struct{})}
		r.flights[key] = f
		r.queue = append(r.queue, f)
	}
	f.callers++
	f.urgent = f.urgent || urgent
	r.startNextLocked()
	r.mu.Unlock()

	// Not yet begun and nobody waits: the page left before its turn. One
	// already running goes on: the next TV asking gets it.
	if _, err := r.wait(ctx, f, func() ([]Result, error) { return nil, f.err }); err != nil {
		return Stream{}, err
	}
	return f.stream, nil
}

// startNextLocked gives yt-dlp the first video someone pressed play on, or
// else the first one asked for.
func (r *Resolver) startNextLocked() {
	if r.busy || len(r.queue) == 0 {
		return
	}
	i := 0
	for j, f := range r.queue {
		if f.urgent {
			i = j
			break
		}
	}
	f := r.queue[i]
	r.queue = append(r.queue[:i], r.queue[i+1:]...)
	f.started = true
	r.busy = true
	go r.resolve(f, f.urgent)
}

func (r *Resolver) dropLocked(f *flight) {
	for i, q := range r.queue {
		if q == f {
			r.queue = append(r.queue[:i], r.queue[i+1:]...)
			break
		}
	}
	if r.flights[f.key] == f {
		delete(r.flights, f.key)
	}
}

// urgent: whether a press started it (f.urgent may change under r.mu meanwhile).
func (r *Resolver) resolve(f *flight, urgent bool) {
	defer func() {
		r.mu.Lock()
		delete(r.flights, f.key)
		switch {
		case f.err != nil, f.swap != nil:
		case f.search != nil:
			now := r.Now()
			for k, e := range r.searches {
				if now.After(e.expires) {
					delete(r.searches, k)
				}
			}
			r.searches[f.key] = searchEntry{results: f.results, expires: now.Add(searchLifetime)}
		default:
			r.cache[f.key] = f.stream
		}
		r.busy = false
		r.startNextLocked()
		r.mu.Unlock()
		close(f.done)
	}()

	if f.swap != nil {
		f.err = f.swap()
		return
	}
	// Not tied to a caller: a TV that gives up doesn't waste the run for the next one.
	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()
	if f.search != nil {
		r.runSearch(ctx, f)
		return
	}
	started := r.Now()
	out, via, err := r.info(ctx, f.key)
	if err != nil {
		r.Log.Warn("yt-dlp", "key", f.key, "via", via, "err", err)
		f.err = fmt.Errorf("%w: %v", ErrUnavailable, err)
		return
	}
	f.stream, f.err = parse(out, r.Now())
	if f.err != nil && !errors.Is(f.err, ErrNoStream) {
		r.Log.Warn("yt-dlp", "key", f.key, "via", via, "err", f.err)
	}
	r.Log.Info("trailer stream", "key", f.key, "ok", f.err == nil, "via", via, "urgent", urgent,
		"queued", started.Sub(f.asked).Round(time.Millisecond), "took", r.Now().Sub(started).Round(time.Millisecond))
}

// info asks the loaded yt-dlp, or runs yt-dlp when it can't be loaded here.
func (r *Resolver) info(ctx context.Context, key string) ([]byte, string, error) {
	if r.Worker != nil {
		out, err := r.Worker.Info(ctx, key)
		if !errors.Is(err, errNoWorker) {
			return out, "worker", err
		}
	}
	out, err := r.Run(ctx, r.Bin, "--no-config", "--no-warnings", "--no-playlist", "--skip-download",
		"--dump-json", "https://www.youtube.com/watch?v="+key)
	return out, "program", err
}

func parse(out []byte, now time.Time) (Stream, error) {
	var info struct {
		Error   string `json:"error"` // the worker's answer when yt-dlp failed
		Formats []struct {
			ManifestURL string `json:"manifest_url"`
			Protocol    string `json:"protocol"`
		} `json:"formats"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return Stream{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if info.Error != "" {
		return Stream{}, fmt.Errorf("%w: %s", ErrUnavailable, info.Error)
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

// UpdateLoop updates yt-dlp now and then every interval: YouTube changes
// break old yt-dlp releases within weeks.
func (r *Resolver) UpdateLoop(ctx context.Context, interval time.Duration) {
	r.init()
	for {
		r.update(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// update lets a copy of yt-dlp update itself (`yt-dlp -U`, the download)
// while trailers go on with the current one; a new release takes its place
// between two yt-dlp runs. yt-dlp reads its modules from its file as it needs
// them, so a file replaced under a running one broke it: a trailer asked during
// the update got "bad local file header".
func (r *Resolver) update(ctx context.Context) {
	r.init()
	next := r.Bin + ".next"
	defer os.Remove(next)
	if err := copyFile(r.Bin, next); err != nil {
		r.Log.Warn("yt-dlp update", "err", err)
		return
	}
	uctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	out, err := r.Run(uctx, next, "--no-config", "-U")
	cancel()
	if err != nil {
		r.Log.Warn("yt-dlp update", "err", err)
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	r.Log.Info("yt-dlp update", "result", lines[len(lines)-1])
	if !sameFile(r.Bin, next) {
		err := r.exclusive(ctx, func() error {
			if r.Worker != nil {
				r.Worker.Restart() // it has the old one loaded
			}
			return os.Rename(next, r.Bin)
		})
		if err != nil {
			r.Log.Warn("yt-dlp update", "err", err)
		}
	}
	if r.Worker != nil {
		r.Worker.Warm() // the first time: it waited for the update
	}
}

// exclusive runs fn while no yt-dlp runs, ahead of the videos and searches
// waiting for one.
func (r *Resolver) exclusive(ctx context.Context, fn func() error) error {
	f := &flight{key: "swap\x00", asked: r.Now(), done: make(chan struct{}), urgent: true, swap: fn}
	r.mu.Lock()
	r.queue = append([]*flight{f}, r.queue...)
	f.callers++
	r.startNextLocked()
	r.mu.Unlock()
	_, err := r.wait(ctx, f, func() ([]Result, error) { return nil, f.err })
	return err
}

func sameFile(a, b string) bool {
	x, err := os.ReadFile(a)
	if err != nil {
		return false
	}
	y, err := os.ReadFile(b)
	return err == nil && bytes.Equal(x, y)
}
