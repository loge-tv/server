package analyzer

import (
	"testing"

	"github.com/emildeev/skiptv/detector/internal/media"
	"github.com/emildeev/skiptv/detector/internal/plex"
)

func ip(i int) *int { return &i }

func TestPickAudio(t *testing.T) {
	cases := []struct {
		name    string
		streams []plex.Stream
		want    string
	}{
		{"russian over default", []plex.Stream{
			{ID: 1, StreamType: 1, Index: ip(0)},
			{ID: 2, StreamType: 2, Index: ip(1), LanguageCode: "eng", Default: true, Channels: 2},
			{ID: 3, StreamType: 2, Index: ip(2), LanguageCode: "rus", Channels: 6},
		}, "0:2"},
		{"fewest channels among russian", []plex.Stream{
			{ID: 2, StreamType: 2, Index: ip(1), LanguageCode: "rus", Channels: 8},
			{ID: 3, StreamType: 2, Index: ip(2), LanguageCode: "rus", Channels: 2},
		}, "0:2"},
		{"default when no russian", []plex.Stream{
			{ID: 2, StreamType: 2, Index: ip(1), LanguageCode: "eng"},
			{ID: 3, StreamType: 2, Index: ip(3), LanguageCode: "fra", Default: true},
		}, "0:3"},
		{"relative index without stream index", []plex.Stream{
			{ID: 2, StreamType: 2, LanguageCode: "eng"},
			{ID: 3, StreamType: 2, LanguageCode: "rus"},
		}, "0:a:1"},
		{"no audio", []plex.Stream{{ID: 1, StreamType: 1}}, ""},
	}
	for _, c := range cases {
		if got := PickAudio(c.streams); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBlackStart(t *testing.T) {
	var frames []media.Frame
	// 0–300 s: film with one dark keyframe at 100 s; 300–480 s: credits on black.
	for t := 0.0; t < 480; t += 2 {
		b := 20
		if t >= 300 || t == 100 {
			b = 95
		}
		if t >= 300 && int(t)%20 == 0 {
			b = 60 // logo between credit blocks
		}
		frames = append(frames, media.Frame{T: t, PBlack: b})
	}
	got, ok := BlackStart(frames, 85, 0.75, 60)
	if !ok || got != 302 {
		t.Fatalf("got %v %v, want 302", got, ok)
	}
	if _, ok := BlackStart(frames[:140], 85, 0.75, 60); ok {
		t.Fatal("found credits in a film without them")
	}
}

func TestStaleWindow(t *testing.T) {
	a := &Analyzer{Cfg: DefaultConfig()}
	// S05E09 fingerprinted while Plex said 1260 s; the file is 1284 s.
	if !stale(810, a.creditsStart(1284)) {
		t.Fatal("window of a 1260 s duration must be stale for 1284 s")
	}
	for _, d := range []float64{1260, 1261.5, 1200} {
		if stale(810, a.creditsStart(d)) {
			t.Fatalf("window must stay valid for %v s", d)
		}
	}
}

// The Big Bang Theory S05E09 (1284 s): ~25 s of credits on black, a white
// vanity card, then black again until the end. Keyframes every ~2.5 s.
func TestBlackStartShortCreditsAtEnd(t *testing.T) {
	scan := []struct {
		t float64
		b int
	}{
		{1199, 33}, {1201, 26}, {1204, 27}, {1206, 36}, {1209, 40}, {1211, 18}, {1214, 13}, {1216, 15},
		{1219, 15}, {1221, 20}, {1224, 15}, {1226, 19}, {1229, 16}, {1231, 20}, {1234, 41}, {1236, 28},
		{1239, 10}, {1241, 23}, {1244, 23}, {1246, 24}, {1249, 25}, {1251, 25}, {1254, 98}, {1256, 98},
		{1259, 98}, {1261, 96}, {1264, 96}, {1266, 94}, {1269, 96}, {1271, 93}, {1274, 92}, {1276, 8},
		{1279, 0}, {1281, 100},
	}
	var frames []media.Frame
	for _, s := range scan {
		frames = append(frames, media.Frame{T: s.t, PBlack: s.b})
	}
	got, ok := BlackStart(frames, 85, 0.75, 60)
	if !ok || got != 1254 {
		t.Fatalf("got %v %v, want 1254", got, ok)
	}
	// A couple of dark shots near the end are not credits.
	for i := range frames {
		if frames[i].T > 1256 && frames[i].T < 1281 {
			frames[i].PBlack = 20
		}
	}
	if got, ok := BlackStart(frames, 85, 0.75, 60); ok {
		t.Fatalf("got credits at %v, want none", got)
	}
}
