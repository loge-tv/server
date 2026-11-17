// Package media runs ffmpeg cheaply: only the needed stretch of one audio
// stream, decoded to 8 kHz mono, or only video keyframes scaled down for
// black-frame detection. Probing is capped so 4K remuxes with many tracks
// don't make ffmpeg balloon.
package media

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/emildeev/skiptv/detector/internal/fingerprint"
)

type FFmpeg struct {
	Bin     string        // ffmpeg binary
	Nice    int           // nice level, 0 = don't renice
	Timeout time.Duration // per invocation
}

func (f *FFmpeg) command(ctx context.Context, args ...string) *exec.Cmd {
	base := []string{"-nostdin", "-hide_banner", "-loglevel", "error",
		"-probesize", "5000000", "-analyzeduration", "5000000"}
	args = append(base, args...)
	if f.Nice > 0 {
		return exec.CommandContext(ctx, "nice", append([]string{"-n", strconv.Itoa(f.Nice), f.Bin}, args...)...)
	}
	return exec.CommandContext(ctx, f.Bin, args...)
}

// DecodeAudio returns [start, start+dur) of the stream as s16 mono PCM at
// fingerprint.SampleRate. streamSpec is an ffmpeg -map spec, e.g. "0:3".
func (f *FFmpeg) DecodeAudio(ctx context.Context, path, streamSpec string, start, dur float64) ([]int16, error) {
	ctx, cancel := context.WithTimeout(ctx, f.Timeout)
	defer cancel()
	cmd := f.command(ctx,
		"-threads", "1",
		"-ss", ftoa(start), "-t", ftoa(dur),
		"-i", path,
		"-map", streamSpec, "-vn", "-sn", "-dn",
		"-ac", "1", "-ar", strconv.Itoa(fingerprint.SampleRate),
		"-f", "s16le", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &limitWriter{w: &stderr, n: 4096}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pcm := make([]int16, 0, int(dur*fingerprint.SampleRate))
	r := bufio.NewReaderSize(out, 64<<10)
	var s [2]byte
	for {
		if _, err := io.ReadFull(r, s[:]); err != nil {
			break
		}
		pcm = append(pcm, int16(binary.LittleEndian.Uint16(s[:])))
	}
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("ffmpeg audio: %w", ctx.Err())
	}
	if werr != nil {
		return nil, fmt.Errorf("ffmpeg audio: %v: %s", werr, strings.TrimSpace(stderr.String()))
	}
	return pcm, nil
}

// Frame is one keyframe: its time in the file and percent of black pixels.
type Frame struct {
	T      float64 `json:"t"`
	PBlack int     `json:"b"`
}

var blackRe = regexp.MustCompile(`pblack:(\d+).*?\bt:([0-9.]+)`)

// BlackFrames decodes keyframes in [start, start+dur) of the first video
// stream and returns, for each, the share of pixels darker than threshold.
func (f *FFmpeg) BlackFrames(ctx context.Context, path string, start, dur float64) ([]Frame, error) {
	ctx, cancel := context.WithTimeout(ctx, f.Timeout)
	defer cancel()
	cmd := f.command(ctx,
		"-threads", "1", "-skip_frame", "nokey",
		"-ss", ftoa(start), "-t", ftoa(dur), "-copyts",
		"-i", path,
		"-map", "0:v:0", "-an", "-sn", "-dn",
		"-vf", "scale=320:-2,blackframe=amount=0:threshold=32",
		"-loglevel", "info",
		"-f", "null", "-")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var frames []Frame
	var tail []string
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := blackRe.FindStringSubmatch(line); m != nil {
			pb, _ := strconv.Atoi(m[1])
			t, _ := strconv.ParseFloat(m[2], 64)
			frames = append(frames, Frame{T: t, PBlack: pb})
			continue
		}
		if len(tail) < 20 {
			tail = append(tail, line)
		}
	}
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("ffmpeg blackframe: %w", ctx.Err())
	}
	if werr != nil {
		return nil, fmt.Errorf("ffmpeg blackframe: %v: %s", werr, strings.Join(tail, " | "))
	}
	if len(frames) == 0 {
		return nil, errors.New("ffmpeg blackframe: no frames decoded")
	}
	return frames, nil
}

func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}
