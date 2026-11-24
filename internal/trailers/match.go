package trailers

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/loge-tv/server/internal/youtube"
)

// Title is what the search's videos are matched against.
type Title struct {
	Name     string // in the language of the trailers looked for
	Year     int    // 0: unknown
	LastYear int    // a show's last season
	// NeedYear: another movie or show has the same name, so a video says
	// which one by its year.
	NeedYear bool
}

// words of a language: what its trailers are called on YouTube, and the words
// that may stand next to a title in a trailer's name. Only Russian for now.
type words struct {
	trailer, teaser string   // prefixes: «трейлер», «трейлеры», «тизер»
	dubbed          []string // dubbing: the voices are in the language
	subtitled       []string // the original voices, subtitles in the language
	near            []string // prefixes of words that may follow or precede the title
	reject          []string // prefixes of words of videos that are not trailers
	foreign         string   // letters of a neighbour language (Ukrainian trailers)
	season          string   // prefix: «сезон», «сезона»
	// Names the apps show (the uploaders' ones repeat the title, often at length).
	nameTrailer, nameTeaser, nameDubbed, nameSubtitled, nameSeason string
}

var languages = map[string]words{
	"ru": {
		trailer:   "трейлер",
		teaser:    "тизер",
		dubbed:    []string{"дубл", "озвуч"},
		subtitled: []string{"субтитр"},
		near: []string{"трейлер", "тизер", "русск", "дубл", "официальн", "финальн", "фильм", "сериал",
			"мульт", "сезон", "озвуч", "субтитр"},
		reject: []string{"обзор", "реакц", "разбор", "интро", "заставк", "пародия", "пародии", "фанатск",
			"концепт", "нейросет", "отрывок", "сцена", "сцены", "клип",
			"фрагмент", "мнение", "review", "reaction", "fanmade", "concept"},
		foreign:       "іїєґ",
		season:        "сезон",
		nameTrailer:   "Трейлер",
		nameTeaser:    "Тизер",
		nameDubbed:    "дубляж",
		nameSubtitled: "с субтитрами",
		nameSeason:    "%d сезон",
	},
}

// query is what YouTube is asked for the title's trailers.
func (w words) query(t Title, show bool) string {
	parts := []string{t.Name}
	if show {
		parts = append(parts, "сериал")
	} else if t.Year > 0 {
		parts = append(parts, strconv.Itoa(t.Year))
	}
	return strings.Join(append(parts, w.trailer), " ")
}

type token struct {
	text string
	gap  string // the punctuation between it and the one before (spaces left out)
	sep  bool   // there is some
}

// tokens: lower-case words and numbers, ё as е.
func tokens(s string) []token {
	var out []token
	var b, gap strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, token{b.String(), gap.String(), gap.Len() > 0})
			b.Reset()
			gap.Reset()
		}
	}
	for _, r := range strings.ToLower(s) {
		switch {
		case r == 'ё':
			b.WriteRune('е')
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			flush()
			if !unicode.IsSpace(r) {
				gap.WriteRune(r)
			}
		}
	}
	flush()
	return out
}

// sequel: a number of a sequel after a title (a year is not one).
func sequel(s string) bool {
	if n, err := strconv.Atoi(s); err == nil {
		return n >= 1 && n < 100
	}
	switch s {
	case "ii", "iii", "iv", "v", "vi", "vii", "viii", "ix", "x":
		return true
	}
	return false
}

func cyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

func hasPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// named: the title's words stand in the video's name on their own — not as
// the start of another one («Книжный вор» is not «Книжный»).
func (w words) named(video, title []token) bool {
	if len(title) == 0 {
		return false
	}
	// A neighbour word that belongs to another name: in the same script as the
	// title, not one of the trailer words.
	other := func(t token) bool {
		return cyrillic(t.text) && !hasPrefix(t.text, w.near)
	}
	// Set apart from the title by nothing, or by a colon or a full stop: a
	// longer name («Книжный вор», «Аватар: Путь воды», «Аватар. Легенда об Аанге»).
	joins := func(gap string) bool {
		return gap == "" || strings.ContainsAny(gap, ":.")
	}
	for i := 0; i+len(title) <= len(video); i++ {
		same := true
		for j, t := range title {
			if video[i+j].text != t.text {
				same = false
				break
			}
		}
		if !same {
			continue
		}
		if i > 0 && joins(video[i].gap) && other(video[i-1]) {
			continue
		}
		end := i + len(title)
		if end < len(video) && joins(video[end].gap) && other(video[end]) {
			continue
		}
		// A sequel: «Гладиатор 2», «Гладиатор II» (not «… 2 сезон»).
		if end < len(video) && video[end].gap == "" && sequel(video[end].text) &&
			!(end+1 < len(video) && strings.HasPrefix(video[end+1].text, w.season)) {
			continue
		}
		return true
	}
	return false
}

// judge a video: ok false when it is not a trailer of the title in the language.
func (w words) judge(v youtube.Result, t Title) (Found, bool) {
	if v.Duration < 20 || v.Duration > 330 {
		return Found{}, false
	}
	if strings.ContainsAny(strings.ToLower(v.Title), w.foreign) {
		return Found{}, false
	}
	video := tokens(v.Title)
	var trailer, isTeaser, dubbed, subtitled bool
	var years []int
	season := 0
	for i, tk := range video {
		if strings.HasPrefix(tk.text, w.season) && season == 0 {
			season = seasonNumber(video, i)
		}
		switch {
		case hasPrefix(tk.text, w.reject):
			return Found{}, false
		case strings.HasPrefix(tk.text, w.trailer):
			trailer = true
		case strings.HasPrefix(tk.text, w.teaser):
			isTeaser = true
		case hasPrefix(tk.text, w.dubbed):
			dubbed = true
		case hasPrefix(tk.text, w.subtitled):
			subtitled = true
		}
		if len(tk.text) == 4 {
			if y, err := strconv.Atoi(tk.text); err == nil && y >= 1900 && y <= 2100 {
				years = append(years, y)
			}
		}
	}
	if !trailer && !isTeaser {
		return Found{}, false
	}
	if !w.named(video, tokens(t.Name)) {
		return Found{}, false
	}
	if t.Year > 0 {
		last := max(t.LastYear, t.Year)
		fits := false
		for _, y := range years {
			fits = fits || (y >= t.Year-1 && y <= last+1)
		}
		if len(years) > 0 && !fits || len(years) == 0 && t.NeedYear {
			return Found{}, false
		}
	}
	return Found{Result: v, Teaser: isTeaser && !trailer, Dubbed: dubbed, Subtitled: subtitled && !dubbed, Season: season}, true
}

// seasonNumber: «2 сезона», «1 й сезон», «сезон 3»; 0 without one.
func seasonNumber(video []token, i int) int {
	number := func(j int) int {
		if j < 0 || j >= len(video) {
			return 0
		}
		n, err := strconv.Atoi(video[j].text)
		if err != nil || n < 1 || n > 50 {
			return 0
		}
		return n
	}
	if n := number(i - 1); n > 0 {
		return n
	}
	if i >= 2 && len([]rune(video[i-1].text)) <= 2 {
		if n := number(i - 2); n > 0 {
			return n
		}
	}
	return number(i + 1)
}

// name of the n-th found trailer of the same kind (n from 1): «Трейлер, дубляж», «Тизер 2».
func (w words) name(f Found, n int) string {
	name := w.nameTrailer
	if f.Teaser {
		name = w.nameTeaser
	}
	if n > 1 {
		name += " " + strconv.Itoa(n)
	}
	var parts []string
	if f.Season > 0 {
		parts = append(parts, fmt.Sprintf(w.nameSeason, f.Season))
	}
	switch {
	case f.Dubbed:
		parts = append(parts, w.nameDubbed)
	case f.Subtitled:
		parts = append(parts, w.nameSubtitled)
	}
	if len(parts) == 0 {
		return name
	}
	if f.Subtitled && f.Season == 0 {
		return name + " " + w.nameSubtitled
	}
	return name + ", " + strings.Join(parts, ", ")
}

// Pick the title's trailers in the language among the search's videos: the
// dubbed ones and those named in the language first, then the subtitled
// ones, each in YouTube's order.
func (w words) pick(found []youtube.Result, t Title) []Found {
	var best, rest []Found
	seen := map[string]bool{}
	for _, v := range found {
		if seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		f, ok := w.judge(v, t)
		if !ok {
			continue
		}
		if !f.Subtitled {
			best = append(best, f)
		} else {
			rest = append(rest, f)
		}
	}
	return append(best, rest...)
}

// Found is a trailer the search found.
type Found struct {
	youtube.Result
	Teaser    bool
	Dubbed    bool // says so
	Subtitled bool // the original voices
	Season    int  // a show's season it says; 0: none
}
