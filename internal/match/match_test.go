package match

import (
	"math"
	"math/rand"
	"testing"

	"github.com/emildeev/skiptv/detector/internal/fingerprint"
)

const sr = fingerprint.SampleRate

// music synthesises sec seconds of note-like audio: random chords that
// change every 0.15–0.6 s, so that unrelated pieces don't fingerprint alike.
func music(rng *rand.Rand, sec float64) []float64 {
	n := int(sec * sr)
	out := make([]float64, n)
	for i := 0; i < n; {
		l := int((0.15 + 0.45*rng.Float64()) * sr)
		var f [3]float64
		var a [3]float64
		for k := range f {
			f[k] = 200 + 2800*rng.Float64()
			a[k] = 2000 + 4000*rng.Float64()
		}
		for t := 0; t < l && i < n; t, i = t+1, i+1 {
			for k := range f {
				out[i] += a[k] * math.Sin(2*math.Pi*f[k]*float64(t)/sr)
			}
		}
	}
	return out
}

func silence(sec float64) []float64 { return make([]float64, int(sec*sr)) }

func render(rng *rand.Rand, gain, noise float64, parts ...[]float64) []int16 {
	var out []int16
	for _, p := range parts {
		for _, v := range p {
			s := v*gain + noise*rng.NormFloat64()
			out = append(out, int16(max(-32768, min(32767, s))))
		}
	}
	return out
}

var opts = Options{
	MaxBitDiff: 6,
	MaxGap:     pts(3.5),
	MinLen:     pts(15),
	MaxLen:     pts(150),
	Candidates: 5,
}

func pts(s float64) int { return int(s / fingerprint.PointDuration) }

func sec(points int) float64 { return float64(points) * fingerprint.PointDuration }

func TestFindSharedIntro(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	intro := music(rng, 40)
	// B's intro starts off the hop grid (10.0137 s) and is quieter and noisier.
	a := fingerprint.Compute(render(rng, 1, 50, music(rng, 60), intro, music(rng, 200)))
	b := fingerprint.Compute(render(rng, 0.7, 150, music(rng, 10.0137), intro, music(rng, 200)))
	p, ok := Find(a, b, opts)
	if !ok {
		t.Fatal("intro not found")
	}
	checkNear(t, "A start", sec(p.A.Start), 60)
	checkNear(t, "A end", sec(p.A.End), 100)
	checkNear(t, "B start", sec(p.B.Start), 10.0137)
	checkNear(t, "B end", sec(p.B.End), 50.0137)
}

func TestNoMatchBetweenUnrelatedAudio(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	a := fingerprint.Compute(render(rng, 1, 50, music(rng, 300)))
	b := fingerprint.Compute(render(rng, 1, 50, music(rng, 300)))
	if p, ok := Find(a, b, opts); ok {
		t.Fatalf("unexpected match %+v (%.1fs)", p, sec(p.A.Len()))
	}
}

func TestSilenceIsNotAMatch(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	a := fingerprint.Compute(render(rng, 1, 0, music(rng, 20), silence(60), music(rng, 20)))
	b := fingerprint.Compute(render(rng, 1, 0, music(rng, 30), silence(60), music(rng, 20)))
	if p, ok := Find(a, b, opts); ok {
		t.Fatalf("silence matched: %+v", p)
	}
}

func TestTooLongSharedAudioRejected(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	common := music(rng, 200)
	a := fingerprint.Compute(render(rng, 1, 50, music(rng, 10), common))
	b := fingerprint.Compute(render(rng, 1, 50, music(rng, 20), common))
	if _, ok := Find(a, b, opts); ok {
		t.Fatal("200 s of shared audio accepted with MaxLen 150 s")
	}
}

func TestConsensus(t *testing.T) {
	// Three siblings agree on ~[100,700), one outlier found a different jingle.
	r, n, ok := Consensus([]Range{{100, 700}, {105, 702}, {98, 695}, {2000, 2300}})
	if !ok || n != 3 {
		t.Fatalf("got %v n=%d ok=%v", r, n, ok)
	}
	if r.Start != 100 || r.End != 700 {
		t.Fatalf("median range %v, want {100 700}", r)
	}
	if _, _, ok := Consensus(nil); ok {
		t.Fatal("empty consensus ok")
	}
}

func checkNear(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1.0 {
		t.Errorf("%s = %.2fs, want %.2fs ±1s", what, got, want)
	}
}
