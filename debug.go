package main

import (
	"context"
	"errors"
	"fmt"
	"math/bits"

	"github.com/emildeev/skiptv/detector/internal/fingerprint"
	"github.com/emildeev/skiptv/detector/internal/match"
	"github.com/emildeev/skiptv/detector/internal/store"
)

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

