// Package trailers gives a title's trailers: TMDB's, and when TMDB has none in
// the language asked, the ones YouTube's search finds in it (TMDB lists few
// dubbed trailers: none for most new titles in Russian).
package trailers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/loge-tv/server/internal/tmdb"
	"github.com/loge-tv/server/internal/youtube"
)

// TMDB is what the finder asks TMDB (tmdb.Client).
type TMDB interface {
	Videos(ctx context.Context, kind tmdb.Kind, id int, language string) ([]tmdb.Video, error)
	Details(ctx context.Context, kind tmdb.Kind, id int, language string) (tmdb.Details, bool, error)
	Search(ctx context.Context, query, language string) ([]tmdb.Title, error)
}

// Searcher asks YouTube's search (youtube.Resolver: one yt-dlp at a time,
// behind the trailers someone pressed play on).
type Searcher interface {
	Search(ctx context.Context, query, lang string, n int) ([]youtube.Result, error)
}

type Finder struct {
	TMDB    TMDB
	YouTube Searcher // nil: TMDB's trailers only
	// Embeddable tells whether a video plays in YouTube's embedded player (the
	// phones play trailers in it); nil: YouTube's oEmbed.
	Embeddable func(ctx context.Context, key string) bool
	HTTP       *http.Client // for oEmbed; nil: a client with a 5 s timeout
	Log        *slog.Logger
	Now        func() time.Time
	// Wait: how long a page waits for the search (the search goes on and
	// its answer is kept); 0: 8 s.
	Wait time.Duration

	mu    sync.Mutex
	cache map[string]entry
}

type entry struct {
	videos  []tmdb.Video
	expires time.Time
}

const (
	// Most found trailers given, ahead of TMDB's.
	maxFound = 2
	// How long the found ones are kept, and how long "none" is.
	foundTTL = 7 * 24 * time.Hour
	noneTTL  = 24 * time.Hour
)

func (f *Finder) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Finder) log() *slog.Logger {
	if f.Log != nil {
		return f.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Videos gives TMDB's trailers, the ones found on YouTube in the language
// first when TMDB has none in it.
func (f *Finder) Videos(ctx context.Context, kind tmdb.Kind, id int, language string) ([]tmdb.Video, error) {
	videos, err := f.TMDB.Videos(ctx, kind, id, language)
	if err != nil {
		return nil, err
	}
	lang, _, _ := strings.Cut(language, "-")
	w, ok := languages[lang]
	if f.YouTube == nil || !ok {
		return videos, nil
	}
	for _, v := range videos {
		if v.Language == lang {
			return videos, nil
		}
	}
	found := f.found(ctx, w, kind, id, language, lang)
	if len(found) == 0 {
		return videos, nil
	}
	have := map[string]bool{}
	for _, v := range found {
		have[v.Key] = true
	}
	out := append([]tmdb.Video{}, found...)
	for _, v := range videos {
		if !have[v.Key] {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *Finder) found(ctx context.Context, w words, kind tmdb.Kind, id int, language, lang string) []tmdb.Video {
	key := string(kind) + ":" + strconv.Itoa(id) + ":" + lang
	now := f.now()
	f.mu.Lock()
	if e, ok := f.cache[key]; ok && now.Before(e.expires) {
		f.mu.Unlock()
		return e.videos
	}
	f.mu.Unlock()

	videos, err := f.search(ctx, w, kind, id, language, lang)
	if err != nil {
		// Not kept: the next page asks again (or gets the search's answer kept
		// by the resolver when it went on after this page stopped waiting).
		f.log().Warn("trailer search", "item", key, "err", err)
		return nil
	}
	ttl := foundTTL
	if len(videos) == 0 {
		ttl = noneTTL
	}
	f.mu.Lock()
	if f.cache == nil {
		f.cache = map[string]entry{}
	}
	for k, e := range f.cache {
		if now.After(e.expires) {
			delete(f.cache, k)
		}
	}
	f.cache[key] = entry{videos: videos, expires: now.Add(ttl)}
	f.mu.Unlock()
	return videos
}

func (f *Finder) search(ctx context.Context, w words, kind tmdb.Kind, id int, language, lang string) ([]tmdb.Video, error) {
	wait := f.Wait
	if wait == 0 {
		wait = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	d, ok, err := f.TMDB.Details(ctx, kind, id, language)
	if err != nil || !ok {
		return nil, err
	}
	t := Title{Name: d.Title, Year: d.Year, LastYear: d.LastYear}
	// Namesakes: «Как ограбить банк» is a film of 2007 and one of 2026.
	if same, err := f.TMDB.Search(ctx, d.Title, language); err == nil {
		name := joined(d.Title)
		for _, o := range same {
			if (o.TMDBID != id || o.Kind != kind) && (joined(o.Title) == name || joined(o.OriginalTitle) == name) {
				t.NeedYear = true
				break
			}
		}
	}
	results, err := f.YouTube.Search(ctx, w.query(t, kind == tmdb.TV), lang, 10)
	if err != nil {
		return nil, err
	}
	picked := w.pick(results, t)
	if len(picked) > 2*maxFound {
		picked = picked[:2*maxFound]
	}
	// Phones play trailers embedded: a video its uploader doesn't let embed is
	// of no use there. Asked all at once (each is a request to YouTube).
	embeddable := make([]bool, len(picked))
	var wg sync.WaitGroup
	for i, p := range picked {
		wg.Add(1)
		go func() {
			defer wg.Done()
			embeddable[i] = f.embeddable(ctx, p.ID)
		}()
	}
	wg.Wait()
	videos := []tmdb.Video{}
	names := map[string]int{}
	for i, p := range picked {
		if !embeddable[i] || len(videos) == maxFound {
			continue
		}
		typ := "Trailer"
		if p.Teaser {
			typ = "Teaser"
		}
		// «Трейлер, дубляж» and «Трейлер 2, дубляж»: one name per kind, numbered.
		plain := w.name(p, 1)
		names[plain]++
		videos = append(videos, tmdb.Video{Key: p.ID, Name: w.name(p, names[plain]), Type: typ, Language: lang})
	}
	f.log().Info("trailer search", "item", string(kind)+":"+strconv.Itoa(id), "title", d.Title, "results", len(results),
		"picked", len(picked), "given", len(videos))
	return videos, nil
}

func (f *Finder) embeddable(ctx context.Context, key string) bool {
	if f.Embeddable != nil {
		return f.Embeddable(ctx, key)
	}
	client := f.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	u := "https://www.youtube.com/oembed?format=json&url=" + url.QueryEscape("https://www.youtube.com/watch?v="+key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return true
	}
	resp, err := client.Do(req)
	if err != nil {
		return true // unknown: kept
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	// 401: embedding turned off; 403: private; 404: gone.
	return resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden &&
		resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest
}

// joined: a name's words, for comparing names.
func joined(s string) string {
	var parts []string
	for _, t := range tokens(s) {
		parts = append(parts, t.text)
	}
	return strings.Join(parts, " ")
}
