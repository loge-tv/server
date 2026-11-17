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
