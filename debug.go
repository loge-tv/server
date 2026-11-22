package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/bits"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/loge-tv/detector/internal/analyzer"
	"github.com/loge-tv/detector/internal/fingerprint"
	"github.com/loge-tv/detector/internal/match"
	"github.com/loge-tv/detector/internal/media"
	"github.com/loge-tv/detector/internal/store"
)

// debugCredits: detector credits <ratingKey>... — finds end credits from
// the end scan (scanning if needed, cached in DB_PATH) and prints them.
func debugCredits(args []string) error {
	st, err := store.Open(env("DB_PATH", "/data/detector.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	an := &analyzer.Analyzer{
		Cfg: analyzer.DefaultConfig(), Store: st,
		FF:    &media.FFmpeg{Bin: env("FFMPEG", "ffmpeg"), Nice: 15, Timeout: 15 * time.Minute},
		Guard: func(context.Context) error { return nil },
		Log:   slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()})),
	}
	for _, k := range args {
		it, ok, err := st.Item(ctx, k)
		if err != nil || !ok {
			fmt.Printf("%s: not found %v\n", k, err)
			continue
		}
		t0 := time.Now()
		s, ok, err := an.EndCredits(ctx, it)
		fmt.Printf("%s\t%s %02d %02d %s\tfound=%v\t%.1f–%.1f\t%s\t%v\t%s\n", k, it.ShowTitle, it.Season, it.Episode, it.Title,
			ok, s.Start, s.End, s.Source, err, time.Since(t0).Round(time.Second))
	}
	return nil
}

// debugFrames: detector frames <ratingKey> <from> <to> — the cached end
// scan keyframes in [from, to]: time, black percent, text lines and rows.
func debugFrames(args []string) error {
	if len(args) != 3 {
		return errors.New("usage: detector frames <ratingKey> <from> <to>")
	}
	st, err := store.Open(env("DB_PATH", "/data/detector.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	sc, ok, err := st.EndScan(context.Background(), args[0])
	if err != nil || !ok {
		return fmt.Errorf("no end scan: %v", err)
	}
	frames := sc.Frames
	fmt.Printf("# from %.1f, file size %d mtime %d\n", sc.Start, sc.File.Size, sc.File.MTime)
	from, _ := strconv.ParseFloat(args[1], 64)
	to, _ := strconv.ParseFloat(args[2], 64)
	sort.Slice(frames, func(i, j int) bool { return frames[i].T < frames[j].T })
	for _, f := range frames {
		if f.T >= from && f.T <= to {
			fmt.Printf("%.1f\t%d\t%d\t%d\t%v\n", f.T, f.PBlack, len(f.Rows), f.Strip, f.Rows)
		}
	}
	return nil
}

// debugSegments: detector segments — every item with its segments as TSV
// (show, season, episode, title, key, type, start, end, source), sorted, to
// diff results before and after a change of the matching rules.
func debugSegments(args []string) error {
	st, err := store.Open(env("DB_PATH", "/data/detector.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	items, err := st.Items(ctx)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	res, err := st.Results(ctx, keys)
	if err != nil {
		return err
	}
	var lines []string
	for _, k := range keys {
		it := items[k]
		head := fmt.Sprintf("%s\t%02d\t%02d\t%s\t%s", it.ShowTitle, it.Season, it.Episode, it.Title, k)
		if len(res[k].Segments) == 0 {
			lines = append(lines, head+"\t-")
		}
		for _, s := range res[k].Segments {
			lines = append(lines, fmt.Sprintf("%s\t%s\t%.1f\t%.1f\t%s", head, s.Type, s.Start, s.End, s.Source))
		}
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}

// debugMatch: detector match intro|credits <ratingKey> <ratingKey>...
// Prints, for each pair, the best shared run with relaxed length limits
// and the bit-difference histogram at that alignment.
func debugMatch(args []string) error {
	if len(args) < 3 {
		return errors.New("usage: detector match intro|credits <ratingKey> <ratingKey>...")
	}
	st, err := store.Open(env("DB_PATH", "/data/detector.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	part := args[0]
	type fpx struct {
		key string
		off float64
		fp  []uint32
	}
	var fps []fpx
	for _, k := range args[1:] {
		f, ok, err := st.Fingerprint(ctx, k, part)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no %s fingerprint for %s", part, k)
		}
		fps = append(fps, fpx{k, f.Offset, fingerprint.Decode(f.Data)})
	}
	p := func(s float64) int { return int(s / fingerprint.PointDuration) }
	sec := func(n int) float64 { return float64(n) * fingerprint.PointDuration }
	o := match.Options{MaxBitDiff: 6, MaxGap: p(3.5), MinLen: p(2), MaxLen: p(3600), Candidates: 5}
	for i := 0; i < len(fps); i++ {
		for j := i + 1; j < len(fps); j++ {
			a, b := fps[i], fps[j]
			r, ok := match.Find(a.fp, b.fp, o)
			if !ok {
				fmt.Printf("%s~%s: no run\n", a.key, b.key)
				continue
			}
			var hist [33]int
			shift := r.A.Start - r.B.Start
			for x := r.A.Start; x < r.A.End; x++ {
				if a.fp[x] != 0 && b.fp[x-shift] != 0 {
					hist[bits.OnesCount32(a.fp[x]^b.fp[x-shift])]++
				}
			}
			fmt.Printf("%s~%s: A %.1f–%.1f B %.1f–%.1f len %.1fs matched %d/%d bits %v\n", a.key, b.key,
				a.off+sec(r.A.Start), a.off+sec(r.A.End), b.off+sec(r.B.Start), b.off+sec(r.B.End),
				sec(r.A.Len()), r.Matched, r.A.Len(), hist[:13])
		}
	}
	return nil
}
