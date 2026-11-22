package analyzer

import (
	"testing"

	"github.com/loge-tv/detector/internal/media"
	"github.com/loge-tv/detector/internal/plex"
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

func TestSceneAfter(t *testing.T) {
	var frames []media.Frame
	add := func(from, to float64, b int) {
		for x := from; x < to; x += 2 {
			frames = append(frames, media.Frame{T: x, PBlack: b})
		}
	}
	add(0, 100, 20)    // film
	add(100, 160, 95)  // credits on black
	add(160, 164, 30)  // 4 s colour logo inside the credits: not a scene
	add(164, 200, 95)  // more credits
	add(200, 230, 25)  // post-credits scene
	add(230, 240, 100) // black to the end
	if got := SceneRuns(frames, 100, 240, 50, 10); len(got) != 1 || got[0] != 200 {
		t.Fatalf("got %v, want [200]", got)
	}
	// A scene running to the end of the file counts too.
	frames = frames[:len(frames)-5]
	if got := SceneRuns(frames, 100, 230, 50, 10); len(got) != 1 || got[0] != 200 {
		t.Fatalf("to the end: got %v, want [200]", got)
	}
	// Credits to the end with only short logos: no scene.
	if got := SceneRuns(frames[:100], 100, 200, 50, 10); len(got) != 0 {
		t.Fatalf("found scenes %v in credits without one", got)
	}
}

// Measured luma spreads (10th–90th percentile, middle half of the frame):
// credits on a blue background in "It Listens" vs. post-credits scenes.
func TestSceneSpread(t *testing.T) {
	cfg := DefaultConfig()
	flat := []float64{.03, .03, .029, .028, .028, .027, .027, .026, .026, .2, .26, .3}
	if median(flat) >= cfg.SceneSpread {
		t.Errorf("coloured credits taken for a scene: %v", median(flat))
	}
	for _, scene := range [][]float64{
		{.33, .27, .21, .2, .18, .21, .21, .22, .22, .24}, // Thunderbolts*
		{.22, .36, .15, .24, .19, .22, .38, .21},          // Paradise S2E3, 8 bit
		{.13, .3, .24, .29, .22, .27, .3, .3},             // Superman
	} {
		if median(scene) < cfg.SceneSpread {
			t.Errorf("scene not recognised: %v", median(scene))
		}
	}
}

// Measured 10th luma percentiles (8 bit gray): the dark ending of "It
// Listens" before its credits vs. credits on black.
func TestOnBlackLevel(t *testing.T) {
	cfg := DefaultConfig()
	lows := func(v ...float64) []media.Luma {
		out := make([]media.Luma, len(v))
		for i, x := range v {
			out[i] = media.Luma{Low: x / 255}
		}
		return out
	}
	for name, l := range map[string][]media.Luma{
		"It Listens, ending":  lows(7, 7, 7, 7, 6, 5, 64, 7, 4, 5, 9, 6, 9, 4, 6),
		"It Listens, hallway": lows(5, 5, 8, 5, 4, 5, 5, 5, 5, 6, 6, 6, 6, 5, 6),
		"TLOU S2E6, black then a dark scene": lows(0, 0, 0, 0, 0, 0, 0, 1, 2, 4, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6,
			5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5),
		"Wicked, light rays": lows(5, 3, 4, 4, 4, 6, 7, 6, 6, 6, 6, 6, 6, 6, 6, 8, 11, 16, 20, 15, 15, 15, 14, 14, 9, 6, 5, 1, 0, 0),
	} {
		if s := OnBlackLevel(l, cfg.BlackLevel); s >= cfg.BlackLevelShare {
			t.Errorf("%s taken for credits: share %v", name, s)
		}
	}
	for name, l := range map[string][]media.Luma{
		"It Listens credits":     lows(2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2),
		"Thunderbolts credits":   lows(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
		"Trassa, after a fade":   lows(10, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
		"Regular Show, end card": lows(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 2, 0),
		"Regular Show S8E9":      lows(2, 2, 2, 3, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 0, 2),
		"Fourth Phase, black 15": lows(15, 15, 15, 15, 15, 15, 15, 15, 15, 15),
		"Scary Movie":            lows(2, 5, 7, 6, 2, 2, 2, 2, 2, 2, 4, 6, 9, 10, 10, 10, 2, 2, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
	} {
		if s := OnBlackLevel(l, cfg.BlackLevel); s < cfg.BlackLevelShare {
			t.Errorf("%s not taken for credits: share %v", name, s)
		}
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
