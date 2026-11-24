package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Result is one video YouTube's search found.
type Result struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Duration float64 `json:"duration"` // seconds; 0: unknown (a live stream)
	Channel  string  `json:"channel"`
}

type searchJob struct {
	query string
	lang  string
	n     int
}

// How long an answer of the search is kept.
const searchLifetime = 24 * time.Hour

// Search gives YouTube's first n videos for query, titles in lang (YouTube's
// hl: an uploader's own translation of a title when there is one). It waits
// behind the videos someone pressed play on, like Prefetch, and an answer is
// kept a day.
func (r *Resolver) Search(ctx context.Context, query, lang string, n int) ([]Result, error) {
	r.init()
	query = strings.Join(strings.Fields(query), " ")
	if query == "" {
		return nil, nil
	}
	if n <= 0 || n > 20 {
		n = 10
	}
	key := "search\x00" + lang + "\x00" + strconv.Itoa(n) + "\x00" + query
	r.mu.Lock()
	if e, ok := r.searches[key]; ok && r.Now().Before(e.expires) {
		r.mu.Unlock()
		return e.results, nil
	}
	f, ok := r.flights[key]
	if !ok {
		f = &flight{key: key, asked: r.Now(), done: make(chan struct{}), search: &searchJob{query, lang, n}}
		r.flights[key] = f
		r.queue = append(r.queue, f)
	}
	f.callers++
	r.startNextLocked()
	r.mu.Unlock()
	return r.wait(ctx, f, func() ([]Result, error) { return f.results, f.err })
}

// wait for a flight to land, or the caller to leave (a flight nobody waits
// for and not yet begun is dropped).
func (r *Resolver) wait(ctx context.Context, f *flight, answer func() ([]Result, error)) ([]Result, error) {
	select {
	case <-f.done:
		return answer()
	case <-ctx.Done():
		r.mu.Lock()
		f.callers--
		if f.callers == 0 && !f.started {
			r.dropLocked(f)
		}
		r.mu.Unlock()
		return nil, ctx.Err()
	}
}

// runSearch asks the loaded yt-dlp, or runs yt-dlp when it can't be loaded here.
func (r *Resolver) runSearch(ctx context.Context, f *flight) {
	job := f.search
	started := r.Now()
	var out []byte
	var err error
	via := "program"
	if r.Worker != nil {
		out, err = r.Worker.Search(ctx, job.query, job.lang, job.n)
		if !errors.Is(err, errNoWorker) {
			via = "worker"
		}
	}
	if via == "program" {
		args := []string{"--no-config", "--no-warnings", "--flat-playlist", "--dump-single-json"}
		if job.lang != "" {
			args = append(args, "--extractor-args", "youtube:lang="+job.lang)
		}
		out, err = r.Run(ctx, r.Bin, append(args, fmt.Sprintf("ytsearch%d:%s", job.n, job.query))...)
	}
	if err == nil {
		f.results, err = parseSearch(out)
	}
	if err != nil {
		r.Log.Warn("youtube search", "query", job.query, "via", via, "err", err)
		f.err = fmt.Errorf("%w: %v", ErrUnavailable, err)
		return
	}
	r.Log.Info("youtube search", "query", job.query, "found", len(f.results), "via", via,
		"queued", started.Sub(f.asked).Round(time.Millisecond), "took", r.Now().Sub(started).Round(time.Millisecond))
}

func parseSearch(out []byte) ([]Result, error) {
	var answer struct {
		Error   string   `json:"error"`
		Entries []Result `json:"entries"`
	}
	if err := json.Unmarshal(out, &answer); err != nil {
		return nil, err
	}
	if answer.Error != "" {
		return nil, errors.New(answer.Error)
	}
	results := make([]Result, 0, len(answer.Entries))
	for _, e := range answer.Entries {
		if e.ID != "" {
			results = append(results, e)
		}
	}
	return results, nil
}
