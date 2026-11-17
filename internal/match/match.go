// Package match finds audio shared by two fingerprints (an intro or credits
// that two episodes of a season have in common) and picks, for every episode
// of a season, the range that its siblings agree on.
package match

import (
	"math/bits"
	"sort"
)

// Options are in fingerprint points unless noted.
type Options struct {
	MaxBitDiff int // points match when they differ in at most this many bits
	MaxGap     int // longest run of non-matching points inside a segment
	MinLen     int // shortest accepted segment
	MaxLen     int // longest accepted segment
	Candidates int // how many top-voted shifts to evaluate
}

// Range is a half-open interval of fingerprint points [Start, End).
type Range struct{ Start, End int }

func (r Range) Len() int { return r.End - r.Start }

// Pair is the common segment found in two fingerprints.
type Pair struct {
	A, B    Range
	Matched int // matching points inside the segment
}

// Find returns the longest segment shared by a and b, if one satisfies opts.
func Find(a, b []uint32, o Options) (Pair, bool) {
	if len(a) == 0 || len(b) == 0 {
		return Pair{}, false
	}
	// Vote for shifts (i - j) via exact point equality.
	index := make(map[uint32][]int32, len(b))
	for j, v := range b {
		if v != 0 {
			index[v] = append(index[v], int32(j))
		}
	}
	votes := map[int]int{}
	for i, v := range a {
		if v == 0 {
			continue
		}
		for _, j := range index[v] {
			votes[i-int(j)]++
		}
	}
	type sv struct{ shift, n int }
	var cand []sv
	for s, n := range votes {
		// smooth: neighbouring shifts belong to the same alignment
		cand = append(cand, sv{s, n + votes[s-1] + votes[s+1]})
	}
	sort.Slice(cand, func(i, j int) bool {
		if cand[i].n != cand[j].n {
			return cand[i].n > cand[j].n
		}
		return cand[i].shift < cand[j].shift
	})
	var best Pair
	found := false
	tried := map[int]bool{}
	for k := 0; k < len(cand) && len(tried) < 3*o.Candidates; k++ {
		if cand[k].n < 2 {
			break
		}
		for d := -1; d <= 1; d++ {
			s := cand[k].shift + d
			if tried[s] {
				continue
			}
			tried[s] = true
			p, ok := longestRun(a, b, s, o)
			if ok && (!found || p.A.Len() > best.A.Len() ||
				(p.A.Len() == best.A.Len() && p.Matched > best.Matched)) {
				best, found = p, true
			}
		}
	}
	return best, found
}

// longestRun aligns a[i] with b[i-shift] and returns the longest run of
// matching points where gaps are at most MaxGap.
func longestRun(a, b []uint32, shift int, o Options) (Pair, bool) {
	lo := max(0, shift)
	hi := min(len(a), len(b)+shift)
	var best Pair
	found := false
	runStart, last, matched := -1, -1, 0
	flush := func() {
		if runStart < 0 {
			return
		}
		r := Range{runStart, last + 1}
		if r.Len() >= o.MinLen && r.Len() <= o.MaxLen && matched*4 >= r.Len() {
			if !found || r.Len() > best.A.Len() {
				best = Pair{A: r, B: Range{r.Start - shift, r.End - shift}, Matched: matched}
				found = true
			}
		}
	}
	for i := lo; i < hi; i++ {
		x, y := a[i], b[i-shift]
		if x == 0 || y == 0 || bits.OnesCount32(x^y) > o.MaxBitDiff {
			continue
		}
		if runStart >= 0 && i-last-1 > o.MaxGap {
			flush()
			runStart = -1
		}
		if runStart < 0 {
			runStart, matched = i, 0
		}
		last = i
		matched++
	}
	flush()
	return best, found
}

// Consensus picks, among ranges one episode shares with its siblings, the
// range most of them agree on: the candidate overlapping (by more than half
// of the shorter one) the most others, then the median start and end of
// that group. ok is false if ranges is empty.
func Consensus(ranges []Range) (Range, int, bool) {
	if len(ranges) == 0 {
		return Range{}, 0, false
	}
	bestI, bestN, bestLen := 0, -1, 0
	groups := make([][]Range, len(ranges))
	for i, r := range ranges {
		for _, q := range ranges {
			if overlaps(r, q) {
				groups[i] = append(groups[i], q)
			}
		}
		n := len(groups[i])
		if n > bestN || (n == bestN && r.Len() > bestLen) {
			bestI, bestN, bestLen = i, n, r.Len()
		}
	}
	g := groups[bestI]
	starts := make([]int, len(g))
	ends := make([]int, len(g))
	for i, r := range g {
		starts[i], ends[i] = r.Start, r.End
	}
	return Range{median(starts), median(ends)}, len(g), true
}

func overlaps(a, b Range) bool {
	ov := min(a.End, b.End) - max(a.Start, b.Start)
	return ov > 0 && 2*ov > min(a.Len(), b.Len())
}

func median(v []int) int {
	sort.Ints(v)
	return v[len(v)/2]
}
