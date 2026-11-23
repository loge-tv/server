package trailers

import (
	"testing"

	"github.com/loge-tv/server/internal/youtube"
)

// Names and lengths of videos YouTube's search found for the titles of a
// user's library (2026-11-23), for which TMDB has no Russian trailer.
func TestJudge(t *testing.T) {
	ru := languages["ru"]
	rob2026 := Title{Name: "Как ограбить банк", Year: 2026, LastYear: 2026, NeedYear: true}
	rob2007 := Title{Name: "Как ограбить банк", Year: 2007, LastYear: 2007, NeedYear: true}
	flavia := Title{Name: "Флавия. Юный детектив", Year: 2026, LastYear: 2026}
	bookish := Title{Name: "Книжный", Year: 2025, LastYear: 2026}
	insideJob := Title{Name: "Корпорация Заговор", Year: 2021, LastYear: 2022}
	greener := Title{Name: "Зеленее травы", Year: 2019, LastYear: 2019}
	cases := []struct {
		title    Title
		video    string
		seconds  float64
		ok       bool
		subtitle bool
	}{
		{rob2026, "Как ограбить банк — Русский трейлер (Дубляж, 2026)", 131, true, false},
		{rob2026, "Как ограбить банк - Русский трейлер (4K Субтитры, 2026) Николас Холт", 184, true, true},
		{rob2026, "🔥 ФИЛЬМ ПРО САМЫЙ ОПАСНЫЙ ПЛАН ОГРАБЛЕНИЯ БАНКА В ИСТОРИИ 🚨 | Триллер 2026", 5196, false, false},
		{rob2026, "How to Rob a Bank 2026 Trailer", 120, false, false},
		{rob2026, "Як пограбувати банк. Офіційний трейлер", 120, false, false},
		// Which of the two films: a namesake's video says it by its year.
		{rob2026, "Как ограбить банк — трейлер на русском (дубляж)", 114, false, false},
		{rob2026, "Как ограбить банк (2024) | Официальный трейлер | Нетфликс", 103, false, false},
		{rob2007, "Как ограбить банк — Русский трейлер (Дубляж, 2026)", 131, false, false},
		{rob2007, "How To Rob a Bank  2007  trailer", 93, false, false},
		{rob2007, "ФИЛЬМ! Как ограбить банк. Комедийный Триллер", 4662, false, false},

		{flavia, "Флавия. Юный детектив | Трейлер | В кино с 30 апреля", 61, true, false},
		{flavia, "Трейлер к фильму « Флавия. Юный детектив (Flavia) » (2026)", 61, true, false},
		{flavia, "Флавия.Юный детектив(Фильм 2026) - Дублированный Русский Трейлер", 61, true, false},
		{flavia, "Флавия Юный детектив — Русский Трейлер (Дубляж, 2026)", 74, true, false},
		{flavia, "Flavia | Official Trailer | Sky Cinema", 84, false, false},
		{flavia, "Официальный трейлер фильма FLAVIA (2026) с Мартином Фрименом и Джонатаном Прайсом в главных ролях", 104, false, false},

		// No Russian trailer of «Книжный» (Bookish) on YouTube: other books' ones and announcements.
		{bookish, "Книжный, Bookish,📚 сериал, с 2025 г.", 51, false, false},
		{bookish, "КНИЖНЫЙ МАГАЗИН — русский трейлер", 129, false, false},
		{bookish, "Книжный в Париже - Русский трейлер (дублированный)", 111, false, false},
		{bookish, "Книжный клуб / Book Club (2018) Дублированный трейлер HD", 154, false, false},
		{bookish, "Книжный вор - Русский трейлер", 141, false, false},
		{bookish, "Книжный  1 серия [Анонс] [Дата выхода]", 84, false, false},
		{bookish, "Bookish, 3 сезон: дата выхода, актерский состав и все, что известно (2026)", 227, false, false},

		{insideJob, "Корпорация Заговор - русский трейлер | Netflix", 127, true, false},
		{insideJob, "Корпорация «Заговор» 😎 Русский трейлер 1 й сезон 😎 Сериал 2021 Netflix", 140, true, false},
		{insideJob, "Inside Job | Корпорация \"Заговор\" | Трейлер 2 сезона на русском", 201, true, false},
		{insideJob, "Интро \" Корпорация заговор\" Заставка.", 23, false, false},
		{insideJob, "Тайны и Шифры \"Корпорации Заговор\" - Нового Мульта от Создателей \"Гравити Фолз\"", 775, false, false},
		{insideJob, "Восход корпорации Vought — Русский трейлер (Субтитры, 2027)", 99, false, false},

		{greener, "Зеленее травы — Русский трейлер (2019)", 137, true, false},
		{greener, "Зеленее травы | Комедия 2019 | трейлер", 152, true, false},
		{greener, "ЗЕЛЕНЕЕ ТРАВЫ Русский Трейлер #1 (2019) Жослин ДеБоэр, Доун Люббе Comedy Movie HD", 147, true, false},
		{greener, "Greener Grass - Official Trailer I HD I IFC Midnight", 134, false, false},
		{greener, "Зеленее травы — реакция на трейлер", 137, false, false},
		{greener, "Зеленее травы — Русский трейлер (2019)", 0, false, false}, // a live stream
	}
	for _, c := range cases {
		f, ok := ru.judge(youtube.Result{ID: "x", Title: c.video, Duration: c.seconds}, c.title)
		if ok != c.ok || ok && f.Subtitled != c.subtitle {
			t.Errorf("%s / %q: ok=%v %+v, want ok=%v subtitled=%v", c.title.Name, c.video, ok, f, c.ok, c.subtitle)
		}
	}
}

func TestPickOrder(t *testing.T) {
	ru := languages["ru"]
	found := []youtube.Result{
		{ID: "subs", Title: "Как ограбить банк - Русский трейлер (4K Субтитры, 2026)", Duration: 184},
		{ID: "en", Title: "How to Rob a Bank — Official Trailer 2 (2026)", Duration: 114},
		{ID: "dub", Title: "Как ограбить банк — Русский трейлер (Дубляж, 2026)", Duration: 131},
		{ID: "dub", Title: "Как ограбить банк — Русский трейлер (Дубляж, 2026)", Duration: 131},
		{ID: "teaser", Title: "Как ограбить банк — Тизер (2026)", Duration: 40},
	}
	picked := ru.pick(found, Title{Name: "Как ограбить банк", Year: 2026, LastYear: 2026})
	var ids []string
	for _, p := range picked {
		ids = append(ids, p.ID)
	}
	if len(ids) != 3 || ids[0] != "dub" || ids[1] != "teaser" || ids[2] != "subs" || !picked[1].Teaser || picked[0].Teaser {
		t.Fatalf("%v", ids)
	}
}

func TestQuery(t *testing.T) {
	ru := languages["ru"]
	if q := ru.query(Title{Name: "Зеленее травы", Year: 2019}, false); q != "Зеленее травы 2019 трейлер" {
		t.Fatal(q)
	}
	if q := ru.query(Title{Name: "Книжный", Year: 2025}, true); q != "Книжный сериал трейлер" {
		t.Fatal(q)
	}
}

func TestNames(t *testing.T) {
	ru := languages["ru"]
	insideJob := Title{Name: "Корпорация Заговор", Year: 2021, LastYear: 2022}
	cases := []struct {
		title Title
		video string
		n     int
		want  string
	}{
		{insideJob, "Корпорация Заговор - русский трейлер | Netflix", 1, "Трейлер"},
		{insideJob, "Корпорация «Заговор» 😎 Русский трейлер 1 й сезон 😎 Сериал 2021 Netflix", 1, "Трейлер, 1 сезон"},
		{insideJob, "Inside Job | Корпорация \"Заговор\" | Трейлер 2 сезона на русском", 2, "Трейлер 2, 2 сезон"},
		{insideJob, "Корпорация Заговор — Тизер, сезон 2 (дубляж)", 1, "Тизер, 2 сезон, дубляж"},
		{Title{Name: "Как ограбить банк", Year: 2026}, "Как ограбить банк — Русский трейлер (Дубляж, 2026)", 1, "Трейлер, дубляж"},
		{Title{Name: "Как ограбить банк", Year: 2026}, "Как ограбить банк - Русский трейлер (4K Субтитры, 2026)", 1, "Трейлер с субтитрами"},
	}
	for _, c := range cases {
		f, ok := ru.judge(youtube.Result{ID: "x", Title: c.video, Duration: 120}, c.title)
		if got := ru.name(f, c.n); !ok || got != c.want {
			t.Errorf("%q: %q, want %q (ok %v)", c.video, got, c.want, ok)
		}
	}
}
