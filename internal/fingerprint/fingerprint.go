// Package fingerprint computes a compact audio fingerprint: one 32-bit
// sub-fingerprint per hop, derived from the sign of energy differences
// between neighbouring frequency bands across consecutive frames
// (Haitsma–Kalker). The same music in two files gives nearly identical
// sub-fingerprints (a few differing bits), unrelated audio gives ~16
// differing bits, which is what the matcher relies on.
package fingerprint

import (
	"math"
	"math/cmplx"
)

const (
	// SampleRate is the PCM rate the decoder must produce (mono, s16le).
	SampleRate = 8000
	// FrameSize is the analysis window in samples (256 ms).
	FrameSize = 2048
	// Hop is the distance between sub-fingerprints in samples (32 ms).
	Hop = 256
	// Bands is the number of frequency bands; it yields Bands-1 = 32 bits.
	Bands = 33
	minFreq = 300.0
	maxFreq = 3000.0
	// silenceRMS: frames quieter than this (≈ -60 dBFS) get value 0,
	// which the matcher never treats as a match.
	silenceRMS = 32.0
)

// PointDuration is the time covered by one sub-fingerprint, in seconds.
const PointDuration = float64(Hop) / SampleRate

var (
	window   [FrameSize]float64
	bandBins [Bands + 1]int
)

func init() {
	for i := range window {
		window[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(FrameSize-1))
	}
	binHz := float64(SampleRate) / FrameSize
	for b := 0; b <= Bands; b++ {
		f := minFreq * math.Pow(maxFreq/minFreq, float64(b)/Bands)
		bandBins[b] = int(math.Round(f / binHz))
	}
}

// Compute returns one sub-fingerprint per Hop samples of pcm (mono, SampleRate).
// Point i covers samples [i*Hop, i*Hop+FrameSize).
func Compute(pcm []int16) []uint32 {
	if len(pcm) < FrameSize {
		return nil
	}
	n := (len(pcm)-FrameSize)/Hop + 1
	out := make([]uint32, n)
	buf := make([]complex128, FrameSize)
	prev := make([]float64, Bands)
	cur := make([]float64, Bands)
	prevOK := false
	for i := 0; i < n; i++ {
		frame := pcm[i*Hop : i*Hop+FrameSize]
		var sq float64
		for k, s := range frame {
			v := float64(s)
			sq += v * v
			buf[k] = complex(v*window[k], 0)
		}
		if math.Sqrt(sq/FrameSize) < silenceRMS {
			out[i] = 0
			prevOK = false
			continue
		}
		fft(buf)
		for b := 0; b < Bands; b++ {
			var e float64
			for k := bandBins[b]; k < bandBins[b+1]; k++ {
				a := cmplx.Abs(buf[k])
				e += a * a
			}
			cur[b] = e
		}
		if prevOK {
			var v uint32
			for b := 0; b < Bands-1; b++ {
				d := (cur[b] - cur[b+1]) - (prev[b] - prev[b+1])
				if d > 0 {
					v |= 1 << uint(b)
				}
			}
			if v == 0 {
				v = 1 // 0 is reserved for silence
			}
			out[i] = v
		}
		prev, cur = cur, prev
		prevOK = true
	}
	return out
}

// fft is an in-place iterative radix-2 FFT; len(a) must be a power of two.
func fft(a []complex128) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		ang := -2 * math.Pi / float64(size)
		wn := complex(math.Cos(ang), math.Sin(ang))
		for start := 0; start < n; start += size {
			w := complex(1, 0)
			for k := 0; k < size/2; k++ {
				u := a[start+k]
				v := a[start+k+size/2] * w
				a[start+k] = u + v
				a[start+k+size/2] = u - v
				w *= wn
			}
		}
	}
}

// Encode/Decode store fingerprints as little-endian bytes.
func Encode(fp []uint32) []byte {
	b := make([]byte, 4*len(fp))
	for i, v := range fp {
		b[4*i] = byte(v)
		b[4*i+1] = byte(v >> 8)
		b[4*i+2] = byte(v >> 16)
		b[4*i+3] = byte(v >> 24)
	}
	return b
}

func Decode(b []byte) []uint32 {
	fp := make([]uint32, len(b)/4)
	for i := range fp {
		fp[i] = uint32(b[4*i]) | uint32(b[4*i+1])<<8 | uint32(b[4*i+2])<<16 | uint32(b[4*i+3])<<24
	}
	return fp
}
