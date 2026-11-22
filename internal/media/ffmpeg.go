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

	"github.com/loge-tv/detector/internal/fingerprint"
)

type FFmpeg struct {
	Bin     string        // ffmpeg binary
	Probe   string        // ffprobe binary
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

// Frame is one keyframe of an end scan: its time in the file, percent of
// black pixels, and the text lines on it (see Lines).
type Frame struct {
	T      float64 `json:"t"`
	PBlack int     `json:"b"`
	Strip  int     `json:"s,omitempty"`
	Rows   []int   `json:"r,omitempty"`
}

var blackRe = regexp.MustCompile(`pblack:(\d+).*?\bt:([0-9.]+)`)

// EndFrames decodes keyframes in [start, start+dur) of the first video
// stream and returns, for each, the share of pixels darker than threshold
// and its text lines — one decode for both.
func (f *FFmpeg) EndFrames(ctx context.Context, path string, start, dur float64) ([]Frame, error) {
	ctx, cancel := context.WithTimeout(ctx, f.Timeout)
	defer cancel()
	cmd := f.command(ctx,
		"-threads", "1", "-skip_frame", "nokey",
		"-ss", ftoa(start), "-t", ftoa(dur), "-copyts",
		"-i", path,
		"-map", "0:v:0", "-an", "-sn", "-dn",
		// yuv420p before blackframe: without it ffmpeg hands blackframe the
		// full-range gray the output asks for, and dark scenes read blacker
		// (Thunderbolts post-credits: 50% instead of 32%).
		"-vf", fmt.Sprintf("scale=%d:%d,format=yuv420p,blackframe=amount=0:threshold=32,format=gray", LineW, LineH),
		"-fps_mode", "passthrough",
		"-loglevel", "info",
		"-f", "rawvideo", "-")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var black []Frame
	var tail []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if m := blackRe.FindStringSubmatch(line); m != nil {
				pb, _ := strconv.Atoi(m[1])
				t, _ := strconv.ParseFloat(m[2], 64)
				black = append(black, Frame{T: t, PBlack: pb})
				continue
			}
			if len(tail) < 20 {
				tail = append(tail, line)
			}
		}
	}()
	type text struct {
		strip int
		rows  []int
	}
	var texts []text
	buf := make([]byte, LineW*LineH)
	r := bufio.NewReaderSize(stdout, len(buf))
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			break
		}
		s, rows := Lines(buf)
		texts = append(texts, text{s, rows})
	}
	<-done
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("ffmpeg end scan: %w", ctx.Err())
	}
	if werr != nil {
		return nil, fmt.Errorf("ffmpeg end scan: %v: %s", werr, strings.Join(tail, " | "))
	}
	// blackframe reports every frame it passes on, in order.
	n := min(len(black), len(texts))
	if n == 0 {
		return nil, errors.New("ffmpeg end scan: no frames decoded")
	}
	frames := black[:n]
	for i := range frames {
		if len(texts[i].rows) > 0 {
			frames[i].Strip, frames[i].Rows = texts[i].strip, texts[i].rows
		}
	}
	return frames, nil
}

// YBITDEPTH is the depth the frame's content needs (1 on a black frame),
// not the format's, so frames are converted to 8 bit gray first.
var statRe = regexp.MustCompile(`signalstats\.Y(LOW|HIGH)=([0-9.]+)`)

// Luma of one keyframe, middle half of the picture (letterbox bars cut
// off), as shares of the full range: Low is the 10th percentile, Spread the
// distance to the 90th.
type Luma struct{ Low, Spread float64 }

// LumaStats decodes keyframes in [start, start+dur) and returns their luma.
// Flat backgrounds (credits on a colour) have a Spread of a few percent,
// scenes 15+; credits on black keep Low exactly at the black level.
func (f *FFmpeg) LumaStats(ctx context.Context, path string, start, dur float64) ([]Luma, error) {
	ctx, cancel := context.WithTimeout(ctx, f.Timeout)
	defer cancel()
	cmd := f.command(ctx,
		"-threads", "1", "-skip_frame", "nokey",
		"-ss", ftoa(start), "-t", ftoa(dur),
		"-i", path,
		"-map", "0:v:0", "-an", "-sn", "-dn",
		"-vf", "scale=320:-2,crop=iw:ih*0.5,format=gray,signalstats,metadata=print",
		"-loglevel", "info",
		"-f", "null", "-")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var out []Luma
	var tail []string
	var low, high float64
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		m := statRe.FindStringSubmatch(line)
		if m == nil {
			if len(tail) < 20 && !strings.Contains(line, "Parsed_metadata") {
				tail = append(tail, line)
			}
			continue
		}
		v, _ := strconv.ParseFloat(m[2], 64)
		switch m[1] {
		case "LOW":
			low = v
		case "HIGH": // printed after LOW of the same frame
			high = v
			out = append(out, Luma{Low: low / 255, Spread: (high - low) / 255})
		}
	}
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("ffmpeg signalstats: %w", ctx.Err())
	}
	if werr != nil {
		return nil, fmt.Errorf("ffmpeg signalstats: %v: %s", werr, strings.Join(tail, " | "))
	}
	if len(out) == 0 {
		return nil, errors.New("ffmpeg signalstats: no frames decoded")
	}
	return out, nil
}

// Duration returns the container duration of the file by ffprobe (Probe,
// "ffprobe" by default).
func (f *FFmpeg) Duration(ctx context.Context, path string) (float64, error) {
	bin := f.Probe
	if bin == "" {
		bin = "ffprobe"
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-v", "error", "-probesize", "5000000", "-analyzeduration", "5000000",
		"-show_entries", "format=duration", "-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe: %w", err)
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
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
