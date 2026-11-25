package trailers

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sepiatv/server/internal/tmdb"
	"github.com/sepiatv/server/internal/youtube"
)

type fakeTMDB struct {
	videos   map[int][]tmdb.Video
	details  map[int]tmdb.Details
	namesake []tmdb.Title
}

func (f fakeTMDB) Videos(_ context.Context, _ tmdb.Kind, id int, _ string) ([]tmdb.Video, error) {
	return f.videos[id], nil
}

func (f fakeTMDB) Details(_ context.Context, _ tmdb.Kind, id int, _ string) (tmdb.Details, bool, error) {
	d, ok := f.details[id]
	return d, ok, nil
}

func (f fakeTMDB) Search(context.Context, string, string) ([]tmdb.Title, error) {
	return f.namesake, nil
}

type fakeYouTube struct {
	calls   atomic.Int32
	query   string
	results []youtube.Result
	err     error
}

func (f *fakeYouTube) Search(_ context.Context, query, lang string, n int) ([]youtube.Result, error) {
	f.calls.Add(1)
	f.query = query
	if lang != "ru" || n != 10 {
		panic("lang " + lang)
	}
	return f.results, f.err
}

var english = tmdb.Video{Key: "en1", Name: "Official Trailer", Type: "Trailer", Language: "en", Official: true}

func robTMDB() fakeTMDB {
	return fakeTMDB{
		videos:  map[int][]tmdb.Video{1437511: {english}},
		details: map[int]tmdb.Details{1437511: {Title: "Как ограбить банк", OriginalTitle: "How to Rob a Bank", Year: 2026, LastYear: 2026}},
		namesake: []tmdb.Title{
			{TMDBID: 1437511, Kind: tmdb.Movie, Title: "Как ограбить банк"},
			{TMDBID: 13534, Kind: tmdb.Movie, Title: "Как ограбить банк"},
		},
	}
}

var robFound = []youtube.Result{
	{ID: "nodate", Title: "Как ограбить банк — трейлер на русском (дубляж)", Duration: 114},
	{ID: "dub1", Title: "Как ограбить банк — Русский трейлер (Дубляж, 2026)", Duration: 131},
	{ID: "noembed", Title: "Как ограбить банк — Русский трейлер #2 (Дубляж, 2026)", Duration: 114},
	{ID: "en1", Title: "Как ограбить банк — Русский трейлер 3 (2026)", Duration: 114},
	{ID: "dub4", Title: "Как ограбить банк — Русский трейлер #4 (Дубляж, 2026)", Duration: 114},
}

func TestFoundGoFirst(t *testing.T) {
	yt := &fakeYouTube{results: robFound}
	f := &Finder{TMDB: robTMDB(), YouTube: yt, Embeddable: func(_ context.Context, key string) bool { return key != "noembed" }}
	videos, err := f.Videos(context.Background(), tmdb.Movie, 1437511, "ru")
	if err != nil {
		t.Fatal(err)
	}
	// Two found ones: the namesake's year rules out the undated one, one can't be
	// embedded, and the most given is two. TMDB's English one is the second
	// found one (the same video): not twice.
	var keys []string
	for _, v := range videos {
		keys = append(keys, v.Key)
	}
	if len(keys) != 2 || keys[0] != "dub1" || keys[1] != "en1" || videos[1].Language != "ru" {
		t.Fatalf("keys %v", keys)
	}
	if videos[0].Language != "ru" || videos[0].Official || videos[0].Name != "Трейлер, дубляж" || videos[1].Name != "Трейлер" {
		t.Fatalf("%+v", videos[0])
	}
	if yt.query != "Как ограбить банк 2026 трейлер" {
		t.Fatalf("query %q", yt.query)
	}
	// Kept: no second search.
	f.Videos(context.Background(), tmdb.Movie, 1437511, "ru-RU")
	if yt.calls.Load() != 1 {
		t.Fatalf("searches %d", yt.calls.Load())
	}
}

func TestNoSearchWhenTMDBHasTheLanguage(t *testing.T) {
	yt := &fakeYouTube{results: robFound}
	tm := robTMDB()
	tm.videos[1437511] = []tmdb.Video{english, {Key: "ru1", Language: "ru", Type: "Trailer"}}
	f := &Finder{TMDB: tm, YouTube: yt}
	videos, _ := f.Videos(context.Background(), tmdb.Movie, 1437511, "ru-RU")
	if len(videos) != 2 || yt.calls.Load() != 0 {
		t.Fatalf("%v searches=%d", videos, yt.calls.Load())
	}
	// English, and languages without trailer words: TMDB's as they are.
	for _, lang := range []string{"en", "en-US", "de", ""} {
		f.Videos(context.Background(), tmdb.Movie, 1437511, lang)
	}
	if yt.calls.Load() != 0 {
		t.Fatalf("searched: %d", yt.calls.Load())
	}
}

func TestNoneFoundAndFailures(t *testing.T) {
	now := time.Unix(1795400000, 0)
	yt := &fakeYouTube{results: []youtube.Result{{ID: "vor", Title: "Книжный вор - Русский трейлер", Duration: 141}}}
	tm := fakeTMDB{
		videos:  map[int][]tmdb.Video{251597: {english}},
		details: map[int]tmdb.Details{251597: {Title: "Книжный", OriginalTitle: "Bookish", Year: 2025, LastYear: 2026}},
	}
	f := &Finder{TMDB: tm, YouTube: yt, Embeddable: func(context.Context, string) bool { return true },
		Now: func() time.Time { return now }}
	videos, err := f.Videos(context.Background(), tmdb.TV, 251597, "ru")
	if err != nil || len(videos) != 1 || videos[0].Key != "en1" || yt.query != "Книжный сериал трейлер" {
		t.Fatalf("%v %v %q", videos, err, yt.query)
	}
	// "None" is kept a day.
	f.Videos(context.Background(), tmdb.TV, 251597, "ru")
	now = now.Add(25 * time.Hour)
	f.Videos(context.Background(), tmdb.TV, 251597, "ru")
	if yt.calls.Load() != 2 {
		t.Fatalf("searches %d", yt.calls.Load())
	}
	// A failed search: TMDB's trailers, and asked again next time.
	yt.err = errors.New("youtube: yt-dlp failed")
	now = now.Add(25 * time.Hour)
	for range 2 {
		if videos, err := f.Videos(context.Background(), tmdb.TV, 251597, "ru"); err != nil || len(videos) != 1 {
			t.Fatalf("%v %v", videos, err)
		}
	}
	if yt.calls.Load() != 4 {
		t.Fatalf("searches %d", yt.calls.Load())
	}
}

func TestTMDBAfterFound(t *testing.T) {
	yt := &fakeYouTube{results: []youtube.Result{{ID: "ru1", Title: "Зеленее травы — Русский трейлер (2019)", Duration: 137}}}
	tm := fakeTMDB{
		videos:  map[int][]tmdb.Video{547009: {english}},
		details: map[int]tmdb.Details{547009: {Title: "Зеленее травы", Year: 2019, LastYear: 2019}},
	}
	f := &Finder{TMDB: tm, YouTube: yt, Embeddable: func(context.Context, string) bool { return true }}
	videos, _ := f.Videos(context.Background(), tmdb.Movie, 547009, "ru")
	if len(videos) != 2 || videos[0].Key != "ru1" || videos[0].Type != "Trailer" || videos[1].Key != "en1" {
		t.Fatalf("%+v", videos)
	}
}

func TestSameNamesNumbered(t *testing.T) {
	yt := &fakeYouTube{results: []youtube.Result{
		{ID: "a", Title: "Зеленее травы — Русский трейлер (2019)", Duration: 137},
		{ID: "b", Title: "Зеленее травы — Русский трейлер #2 (2019)", Duration: 139},
	}}
	tm := fakeTMDB{details: map[int]tmdb.Details{547009: {Title: "Зеленее травы", Year: 2019, LastYear: 2019}}}
	f := &Finder{TMDB: tm, YouTube: yt, Embeddable: func(context.Context, string) bool { return true }}
	videos, _ := f.Videos(context.Background(), tmdb.Movie, 547009, "ru")
	if len(videos) != 2 || videos[0].Name != "Трейлер" || videos[1].Name != "Трейлер 2" {
		t.Fatalf("%+v", videos)
	}
}
