// Package analyzer syncs the Plex library into the store and works through
// it one file at a time: fingerprints the start and the end of every
// episode, matches episodes of a season against each other for intros and
// credits, and falls back to black frames for credits of movies and of
// episodes whose ending nothing matched.
package analyzer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emildeev/skiptv/detector/internal/fingerprint"
	"github.com/emildeev/skiptv/detector/internal/match"
	"github.com/emildeev/skiptv/detector/internal/media"
	"github.com/emildeev/skiptv/detector/internal/plex"
	"github.com/emildeev/skiptv/detector/internal/store"
)

type Config struct {
	IntroPercent    float64 // share of the episode searched for the intro
	IntroLimit      float64 // ...but at most this many seconds
	IntroMin        float64
	IntroMax        float64
	CreditsWindow   float64 // seconds at the end of an episode searched for credits
	CreditsMin      float64
	CreditsMax      float64
	MovieCredits    float64 // seconds at the end of a movie scanned for black frames
	MaxAttempts     int
	BlackPercent    int     // keyframe counts as black at this share of dark pixels
	BlackShare      float64 // share of black keyframes needed after credits start
	BlackSpan       float64 // ...over this many seconds
	ScenePercent    int     // keyframe darker than this share is picture, not credits
	SceneMin        float64 // picture this long after credits start ends them
	SceneProbe      float64 // seconds decoded to tell a scene from coloured credits
	SceneSpread     float64 // median luma spread (0–1) of a scene
	BlackLevel      float64 // 10th luma percentile (0–1, gray) at most this is black
	BlackLevelShare float64 // share of keyframes at black level for credits on black
	MaxCreditChecks int     // credits start candidates probed per item
	EndSnap         float64 // credits ending this close to the end run to the end
	MaxBitDiff      int
	MaxGap          float64
	MinFingerprint  float64 // a fingerprint with less non-silent audio is an error
}

// Version of the matching rules. Bump it when they change: results of an
// older version are recomputed on the next pass from the cached
// fingerprints and black frame scans (no ffmpeg).
//
//	2 — black frame credits may be shorter than BlackSpan at the very end;
//	    caches taken with a too short Plex duration are redone.
//	3 — black frame credits end where a scene after them starts.
//	4 — a dark ending of the film is not credits on black.
//	5 — luma probes fixed (were scaled by the content's bit depth).
//	6 — black level is absolute (≤ 3 of 255), not relative to the lowest.
const Version = 6

func DefaultConfig() Config {
	return Config{
		IntroPercent: 0.25, IntroLimit: 600, IntroMin: 15, IntroMax: 120,
		CreditsWindow: 450, CreditsMin: 15, CreditsMax: 450,
		MovieCredits: 1200,
		MaxAttempts:  3,
		BlackPercent: 85, BlackShare: 0.75, BlackSpan: 60,
		ScenePercent: 50, SceneMin: 10, SceneProbe: 30, SceneSpread: 0.08,
		BlackLevel: 3.5 / 255, BlackLevelShare: 0.6, MaxCreditChecks: 30,
		EndSnap:    15,
		MaxBitDiff: 6, MaxGap: 3.5,
		MinFingerprint: 10,
	}
}

type Analyzer struct {
	Cfg    Config
	Plex   *plex.Client
	Store  *store.Store
	FF     *media.FFmpeg
	Guard  func(ctx context.Context) error // blocks while the node is short of memory
	Log    *slog.Logger
	Filter string // only analyse items whose path contains this (debug)
}

// Sync mirrors the movie and show libraries into the store.
func (a *Analyzer) Sync(ctx context.Context) error {
	sections, err := a.Plex.Sections(ctx)
	if err != nil {
		return err
	}
	known, err := a.Store.Items(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, sec := range sections {
		typ := 1
		if sec.Type == "show" {
			typ = 4
		}
		items, err := a.Plex.Items(ctx, sec.Key, typ)
		if err != nil {
			return fmt.Errorf("section %s: %w", sec.Title, err)
		}
		for _, md := range items {
			if len(md.Media) == 0 || len(md.Media[0].Part) == 0 || md.Media[0].Part[0].File == "" {
				continue
			}
			part := md.Media[0].Part[0]
			dur := float64(md.Duration) / 1000
			if part.Duration > 0 {
				dur = float64(part.Duration) / 1000
			}
			it := store.Item{
				RatingKey: md.RatingKey, Kind: md.Type, Title: md.Title,
				Path: part.File, Size: part.Size, Duration: dur,
			}
			if md.Type == "episode" {
				it.SeasonKey, it.ShowTitle, it.Season, it.Episode = md.ParentRatingKey, md.GrandparentTitle, md.ParentIndex, md.Index
			}
			if old, ok := known[md.RatingKey]; ok && old.Path == it.Path && old.Size == it.Size && old.Title == it.Title &&
				old.SeasonKey == it.SeasonKey && old.Episode == it.Episode && old.Duration == it.Duration {
				seen[md.RatingKey] = true
				continue
			}
			if err := a.Store.UpsertItem(ctx, it); err != nil {
				return err
			}
			seen[md.RatingKey] = true
		}
	}
	var gone []string
	for k := range known {
		if !seen[k] {
			gone = append(gone, k)
		}
	}
	if len(gone) > 0 {
		a.Log.Info("items removed from plex", "count", len(gone))
	}
	return a.Store.DeleteItems(ctx, gone)
}

// Work analyses everything pending. It returns when done or ctx ends.
func (a *Analyzer) Work(ctx context.Context) error {
	items, err := a.Store.Items(ctx)
	if err != nil {
		return err
	}
	seasons := map[string][]store.Item{}
	var movies []store.Item
	for _, it := range items {
		if it.Kind == "episode" {
			seasons[it.SeasonKey] = append(seasons[it.SeasonKey], it)
		} else {
			movies = append(movies, it)
		}
	}
	var keys []string
	for k, eps := range seasons {
		if a.pending(eps...) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		x, y := seasons[keys[i]][0], seasons[keys[j]][0]
		if x.ShowTitle != y.ShowTitle {
			return x.ShowTitle < y.ShowTitle
		}
		return x.Season < y.Season
	})
	for _, k := range keys {
		if err := a.season(ctx, seasons[k]); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			a.Log.Error("season", "show", seasons[k][0].ShowTitle, "season", seasons[k][0].Season, "err", err)
		}
	}
	sort.Slice(movies, func(i, j int) bool { return movies[i].Title < movies[j].Title })
	for _, m := range movies {
		if !a.pending(m) {
			continue
		}
		if err := a.movie(ctx, m); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			a.Log.Error("movie", "title", m.Title, "err", err)
		}
	}
	return nil
}

func (a *Analyzer) pending(items ...store.Item) bool {
	for _, it := range items {
		outdated := !it.Analyzed || it.Algo < Version
		if outdated && it.Attempts < a.Cfg.MaxAttempts && (a.Filter == "" || strings.Contains(it.Path, a.Filter)) {
			return true
		}
	}
	return false
}

func (a *Analyzer) fail(ctx context.Context, it store.Item, err error) {
	a.Log.Warn("analysis failed", "key", it.RatingKey, "path", it.Path, "attempt", it.Attempts+1, "err", err)
	if ferr := a.Store.Fail(context.WithoutCancel(ctx), it.RatingKey, err); ferr != nil {
		a.Log.Error("store failure", "err", ferr)
	}
}

// audioSpec picks the Russian audio track (fewest channels among them),
// else the default one, else the first, and remembers it.
func (a *Analyzer) audioSpec(ctx context.Context, it *store.Item) error {
	if it.Audio != "" {
		return nil
	}
	md, err := a.Plex.Item(ctx, it.RatingKey)
	if err != nil {
		return err
	}
	if len(md.Media) == 0 || len(md.Media[0].Part) == 0 {
		return errors.New("no media parts")
	}
	spec := PickAudio(md.Media[0].Part[0].Stream)
	if spec == "" {
		return errors.New("no audio stream")
	}
	it.Audio = spec
	return a.Store.SetAudio(ctx, it.RatingKey, spec)
}

func PickAudio(streams []plex.Stream) string {
	var audio []plex.Stream
	for _, s := range streams {
		if s.StreamType == 2 {
			audio = append(audio, s)
		}
	}
	if len(audio) == 0 {
		return ""
	}
	ord := map[int]int{}
	for i, s := range audio {
		ord[s.ID] = i
	}
	best := -1
	for i, s := range audio {
		rus := s.LanguageCode == "rus" || strings.EqualFold(s.Language, "русский")
		if !rus {
			continue
		}
		if best < 0 || s.Channels < audio[best].Channels {
			best = i
		}
	}
	if best < 0 {
		for i, s := range audio {
			if s.Default {
				best = i
				break
			}
		}
	}
	if best < 0 {
		best = 0
	}
	if audio[best].Index != nil {
		return "0:" + strconv.Itoa(*audio[best].Index)
	}
	return "0:a:" + strconv.Itoa(ord[audio[best].ID])
}

func (a *Analyzer) introWindow(dur float64) float64 {
	return min(dur*a.Cfg.IntroPercent, a.Cfg.IntroLimit)
}

func (a *Analyzer) creditsStart(dur float64) float64 {
	return max(dur-a.Cfg.CreditsWindow, dur*0.5)
}

// fingerprintEpisode computes and stores both fingerprints of an episode.
func (a *Analyzer) fingerprintEpisode(ctx context.Context, it *store.Item) error {
	if err := a.audioSpec(ctx, it); err != nil {
		return err
	}
	parts := []struct {
		name       string
		start, dur float64
	}{
		{"intro", 0, a.introWindow(it.Duration)},
		{"credits", a.creditsStart(it.Duration), it.Duration - a.creditsStart(it.Duration)},
	}
	fps := make([]store.Fingerprint, len(parts))
	for i, p := range parts {
		if err := a.Guard(ctx); err != nil {
			return err
		}
		t0 := time.Now()
		pcm, err := a.FF.DecodeAudio(ctx, it.Path, it.Audio, p.start, p.dur)
		if err != nil {
			return fmt.Errorf("%s: %w", p.name, err)
		}
		fp := fingerprint.Compute(pcm)
		nonzero := 0
		for _, v := range fp {
			if v != 0 {
				nonzero++
			}
		}
		if float64(nonzero)*fingerprint.PointDuration < a.Cfg.MinFingerprint {
			return fmt.Errorf("%s: empty fingerprint (%.0fs of audio, %d points)", p.name,
				float64(len(pcm))/fingerprint.SampleRate, nonzero)
		}
		a.Log.Debug("fingerprint", "key", it.RatingKey, "part", p.name, "points", len(fp), "took", time.Since(t0).Round(time.Millisecond))
		fps[i] = store.Fingerprint{Offset: p.start, Data: fingerprint.Encode(fp)}
	}
	for i, p := range parts {
		if err := a.Store.PutFingerprint(ctx, it.RatingKey, p.name, fps[i]); err != nil {
			return err
		}
	}
	return nil
}

type loaded struct {
	item  store.Item
	intro []uint32
	cred  []uint32
	coff  float64
}

func (a *Analyzer) opts(minS, maxS float64) match.Options {
	p := func(s float64) int { return int(s / fingerprint.PointDuration) }
	return match.Options{MaxBitDiff: a.Cfg.MaxBitDiff, MaxGap: p(a.Cfg.MaxGap), MinLen: p(minS), MaxLen: p(maxS), Candidates: 5}
}

func (a *Analyzer) season(ctx context.Context, eps []store.Item) error {
	sort.Slice(eps, func(i, j int) bool { return eps[i].Episode < eps[j].Episode })
	show, num := eps[0].ShowTitle, eps[0].Season
	t0 := time.Now()
	var ready []loaded
	for i := range eps {
		it := &eps[i]
		fc, have, err := a.Store.Fingerprint(ctx, it.RatingKey, "credits")
		if err != nil {
			return err
		}
		// Taken while Plex reported a shorter duration: misses the real end.
		short := have && stale(fc.Offset, a.creditsStart(it.Duration))
		if !have || short {
			if !short && (it.Analyzed || it.Attempts >= a.Cfg.MaxAttempts) {
				continue
			}
			if err := a.fingerprintEpisode(ctx, it); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				a.fail(ctx, *it, err)
				continue
			}
		}
		fi, _, err := a.Store.Fingerprint(ctx, it.RatingKey, "intro")
		if err != nil {
			return err
		}
		fc, _, err = a.Store.Fingerprint(ctx, it.RatingKey, "credits")
		if err != nil {
			return err
		}
		ready = append(ready, loaded{item: *it, intro: fingerprint.Decode(fi.Data), cred: fingerprint.Decode(fc.Data), coff: fc.Offset})
	}
	intros := a.matchAll(ready, func(l loaded) []uint32 { return l.intro }, a.opts(a.Cfg.IntroMin, a.Cfg.IntroMax))
	credits := a.matchAll(ready, func(l loaded) []uint32 { return l.cred }, a.opts(a.Cfg.CreditsMin, a.Cfg.CreditsMax))
	nIntro, nCred, nBlack := 0, 0, 0
	for i, l := range ready {
		var segs []store.Segment
		if r, ok := intros[i]; ok {
			segs = append(segs, store.Segment{Type: "intro", Start: sec(r.Start), End: sec(r.End), Source: "fingerprint"})
			nIntro++
		}
		if r, ok := credits[i]; ok {
			s := store.Segment{Type: "credits", Start: l.coff + sec(r.Start), End: l.coff + sec(r.End), Source: "fingerprint"}
			if l.item.Duration-s.End <= a.Cfg.EndSnap {
				s.End = l.item.Duration
			}
			segs = append(segs, s)
			nCred++
		} else {
			start := a.creditsStart(l.item.Duration)
			s, ok, err := a.blackCredits(ctx, l.item, start)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				a.Log.Warn("blackframe", "key", l.item.RatingKey, "path", l.item.Path, "err", err)
			} else if ok {
				segs = append(segs, s)
				nBlack++
			}
		}
		if err := a.Store.SetResult(ctx, l.item.RatingKey, Version, segs); err != nil {
			return err
		}
	}
	a.Log.Info("season analyzed", "show", show, "season", num, "episodes", len(eps), "fingerprinted", len(ready),
		"intro", nIntro, "credits", nCred, "creditsBlack", nBlack, "took", time.Since(t0).Round(time.Second))
	return nil
}

// matchAll compares every episode with every other one and returns, per
// episode index, the range its siblings agree on.
func (a *Analyzer) matchAll(ls []loaded, fp func(loaded) []uint32, o match.Options) map[int]match.Range {
	found := make([][]match.Range, len(ls))
	for i := 0; i < len(ls); i++ {
		for j := i + 1; j < len(ls); j++ {
			if p, ok := match.Find(fp(ls[i]), fp(ls[j]), o); ok {
				found[i] = append(found[i], p.A)
				found[j] = append(found[j], p.B)
			}
		}
	}
	out := map[int]match.Range{}
	for i, rs := range found {
		if r, _, ok := match.Consensus(rs); ok {
			out[i] = r
		}
	}
	return out
}

func sec(points int) float64 {
	return float64(int(float64(points)*fingerprint.PointDuration*10+0.5)) / 10
}

// blackCredits scans (or reuses a scan of) keyframes from start to the end
// and returns credits starting at the first sustained run of black frames.
func (a *Analyzer) blackCredits(ctx context.Context, it store.Item, start float64) (store.Segment, bool, error) {
	frames, from, have, err := a.Store.BlackScan(ctx, it.RatingKey)
	if err != nil {
		return store.Segment{}, false, err
	}
	if !have || stale(from, start) {
		if err := a.Guard(ctx); err != nil {
			return store.Segment{}, false, err
		}
		t0 := time.Now()
		frames, err = a.FF.BlackFrames(ctx, it.Path, start, it.Duration-start)
		if err != nil {
			return store.Segment{}, false, err
		}
		a.Log.Debug("blackframe scan", "key", it.RatingKey, "frames", len(frames), "took", time.Since(t0).Round(time.Second))
		if err := a.Store.PutBlackScan(ctx, it.RatingKey, start, frames); err != nil {
			return store.Segment{}, false, err
		}
	}
	// Plex sometimes under-reports the duration (Kraven .m2ts: 1094 s of
	// 7616): the scan itself shows how long the file is.
	end := it.Duration
	if len(frames) > 0 {
		end = max(end, frames[len(frames)-1].T)
	}
	// A dark ending of the film is not credits on black: on black the
	// lowest luma stays at the black level (checked by ffmpeg).
	t, ok, rejected, checks := 0.0, false, math.Inf(-1), 0
	for _, c := range BlackStarts(frames, a.Cfg.BlackPercent, a.Cfg.BlackShare, a.Cfg.BlackSpan) {
		if end-c < a.Cfg.CreditsMin || checks >= a.Cfg.MaxCreditChecks {
			break
		}
		if c < rejected+a.Cfg.SceneProbe {
			continue
		}
		checks++
		l, err := a.luma(ctx, it, c)
		if err != nil {
			if ctx.Err() != nil {
				return store.Segment{}, false, ctx.Err()
			}
			a.Log.Warn("credits check", "key", it.RatingKey, "at", c, "err", err)
			t, ok = c, true // as before the check
			break
		}
		if share := OnBlackLevel(l, a.Cfg.BlackLevel); share >= a.Cfg.BlackLevelShare {
			t, ok = c, true
			break
		} else {
			a.Log.Info("dark picture, not credits", "key", it.RatingKey, "at", c, "share", share)
		}
		rejected = c
	}
	if !ok {
		return store.Segment{}, false, nil
	}
	// A scene after the credits (post-credits, mid-credits) is not skipped.
	// Studio logos in the last EndSnap seconds are not scenes, and neither
	// are credits on a colour background (flat picture, checked by ffmpeg).
	for _, s := range SceneRuns(frames, t, end, a.Cfg.ScenePercent, a.Cfg.SceneMin) {
		if s-t < a.Cfg.CreditsMin || end-s <= a.Cfg.EndSnap {
			continue
		}
		scene, err := a.isScene(ctx, it, s)
		if err != nil {
			if ctx.Err() != nil {
				return store.Segment{}, false, ctx.Err()
			}
			a.Log.Warn("scene check", "key", it.RatingKey, "at", s, "err", err)
			continue
		}
		if scene {
			end = float64(int(s*10)) / 10
			break
		}
	}
	return store.Segment{Type: "credits", Start: float64(int(t*10)) / 10, End: end, Source: "blackframe"}, true, nil
}

// luma probes (or reuses a probe of) SceneProbe seconds of keyframes from s.
func (a *Analyzer) luma(ctx context.Context, it store.Item, s float64) ([]media.Luma, error) {
	l, have, err := a.Store.LumaCheck(ctx, it.RatingKey, s)
	if err != nil || have {
		return l, err
	}
	if err := a.Guard(ctx); err != nil {
		return nil, err
	}
	l, err = a.FF.LumaStats(ctx, it.Path, s, a.Cfg.SceneProbe)
	if err != nil {
		return nil, err
	}
	return l, a.Store.PutLumaCheck(ctx, it.RatingKey, s, l)
}

// isScene: a scene has textured picture, credits a flat background.
func (a *Analyzer) isScene(ctx context.Context, it store.Item, s float64) (bool, error) {
	l, err := a.luma(ctx, it, s)
	if err != nil {
		return false, err
	}
	sp := make([]float64, len(l))
	for i, x := range l {
		sp[i] = x.Spread
	}
	a.Log.Debug("scene check", "key", it.RatingKey, "at", s, "spread", median(sp))
	return median(sp) >= a.Cfg.SceneSpread, nil
}

// OnBlackLevel returns the share of keyframes whose 10th luma percentile is
// at most level: nearly all of them for credits on black (0–2 of 255), few
// for a dark scene (4–9).
func OnBlackLevel(l []media.Luma, level float64) float64 {
	if len(l) == 0 {
		return 0
	}
	n := 0
	for _, x := range l {
		if x.Low <= level {
			n++
		}
	}
	return float64(n) / float64(len(l))
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// SceneRuns returns the starts of runs of picture keyframes (dark share
// below pct) after from that last at least min seconds: until the next
// darker keyframe, or end. frames must be sorted by time.
func SceneRuns(frames []media.Frame, from, end float64, pct int, min float64) []float64 {
	var out []float64
	runStart, n := 0.0, 0
	for _, f := range frames {
		if f.T <= from {
			continue
		}
		if f.PBlack < pct {
			if n == 0 {
				runStart = f.T
			}
			n++
			continue
		}
		if n >= 2 && f.T-runStart >= min {
			out = append(out, runStart)
		}
		n = 0
	}
	if n >= 2 && end-runStart >= min {
		out = append(out, runStart)
	}
	return out
}

// stale reports whether a cached end-of-file window that started at cached
// should have started at want: the duration grew since, so the cache does
// not reach the real end of the file. Shrinking is fine (ffmpeg stops at
// EOF), and Plex's numbers wobble by a second or two.
func stale(cached, want float64) bool {
	return want > cached+store.DurationSlack/2
}

// BlackStart returns the time of the first black keyframe after which at
// least share of the keyframes within span seconds are black too. Closer
// than span to the end of the scan the share is taken over the frames left:
// short sitcom credits (~25 s on black, then a bright vanity card) count.
func BlackStart(frames []media.Frame, pct int, share, span float64) (float64, bool) {
	if s := BlackStarts(frames, pct, share, span); len(s) > 0 {
		return s[0], true
	}
	return 0, false
}

// BlackStarts returns every keyframe time BlackStart would accept, in order.
func BlackStarts(frames []media.Frame, pct int, share, span float64) []float64 {
	sort.Slice(frames, func(i, j int) bool { return frames[i].T < frames[j].T })
	var out []float64
	for i, f := range frames {
		if f.PBlack < pct {
			continue
		}
		total, black := 0, 0
		for _, g := range frames[i:] {
			if g.T > f.T+span {
				break
			}
			total++
			if g.PBlack >= pct {
				black++
			}
		}
		if total >= 3 && float64(black) >= share*float64(total) {
			out = append(out, f.T)
		}
	}
	return out
}

func (a *Analyzer) movie(ctx context.Context, it store.Item) error {
	t0 := time.Now()
	start := max(it.Duration-a.Cfg.MovieCredits, it.Duration*0.75)
	s, ok, err := a.blackCredits(ctx, it, start)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		a.fail(ctx, it, err)
		return nil
	}
	var segs []store.Segment
	if ok {
		segs = append(segs, s)
	}
	a.Log.Info("movie analyzed", "title", it.Title, "credits", ok, "start", s.Start, "took", time.Since(t0).Round(time.Second))
	return a.Store.SetResult(ctx, it.RatingKey, Version, segs)
}
