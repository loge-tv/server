package tmdb

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// NameQuery is a catalog title Plex has only in English (or in its original
// language): its kind, its catalog name, its year, and its TMDB id when the
// client knows it.
type NameQuery struct {
	Kind   Kind
	TMDBID int // 0: unknown, found by Title and Year
	Title  string
	Year   int // 0: unknown (then only an id finds it)
}

// Name is a title's name in the language asked. Title is empty when TMDB has
// no translation, or the query matched no title for sure.
type Name struct {
	TMDBID int
	Title  string
}

type nameEntry struct {
	name    Name
	expires time.Time
}

const (
	nameHitTTL  = 30 * 24 * time.Hour
	nameMissTTL = 24 * time.Hour // unreleased titles get their translations later
	maxNames    = 20000
)

// LocalName finds a title's name in language. Without an id it searches
// TMDB in English for the same kind, the same name (TMDB's English or
// original one, case and accents aside) and the same year — one year off
// only when nothing else is near: a wrong name is worse than an English one.
// Names are cached: found ones for a month, misses for a day.
func (c *Client) LocalName(ctx context.Context, q NameQuery, language string) (Name, error) {
	if q.Kind != Movie && q.Kind != TV {
		return Name{}, fmt.Errorf("tmdb: unknown kind %q", q.Kind)
	}
	key := fmt.Sprintf("%s|%d|%d|%s|%s", q.Kind, q.TMDBID, q.Year, fold(q.Title), language)
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.names[key]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e.name, nil
	}
	c.mu.Unlock()

	id := q.TMDBID
	if id == 0 {
		found, err := c.find(ctx, q)
		if err != nil {
			return Name{}, err
		}
		id = found
	}
	var name Name
	if id != 0 {
		d, ok, err := c.Details(ctx, q.Kind, id, language)
		if err != nil {
			return Name{}, err
		}
		if ok {
			name.TMDBID = id
			if translated(d, language) {
				name.Title = d.Title
			}
		}
	}

	ttl := nameMissTTL
	if name.Title != "" {
		ttl = nameHitTTL
	}
	c.mu.Lock()
	if c.names == nil {
		c.names = map[string]nameEntry{}
	}
	if len(c.names) >= maxNames {
		for k, e := range c.names {
			if now.After(e.expires) || len(c.names) >= maxNames {
				delete(c.names, k)
			}
		}
	}
	c.names[key] = nameEntry{name: name, expires: now.Add(ttl)}
	c.mu.Unlock()
	return name, nil
}

// find searches TMDB in English for the query's title: the id, or 0.
func (c *Client) find(ctx context.Context, q NameQuery) (int, error) {
	if q.Year == 0 || strings.TrimSpace(q.Title) == "" {
		return 0, nil
	}
	v := url.Values{"query": {strings.Join(strings.Fields(q.Title), " ")}, "page": {"1"}, "language": {"en-US"}}
	var body struct {
		Results []result `json:"results"`
	}
	status, err := c.get(ctx, "/search/"+string(q.Kind), v, &body)
	if err != nil || status == http.StatusNotFound {
		return 0, err
	}
	want := fold(q.Title)
	var exact, near []int
	for _, r := range body.Results {
		t, ok := r.title(q.Kind)
		if !ok || (fold(t.Title) != want && fold(t.OriginalTitle) != want) {
			continue
		}
		switch t.Year {
		case q.Year:
			exact = append(exact, t.TMDBID)
		case q.Year - 1, q.Year + 1:
			near = append(near, t.TMDBID)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) == 0 && len(near) == 1:
		return near[0], nil
	}
	return 0, nil // none, or several: not sure which
}

// fold: lower case, single spaces, ё = е, typographic quotes and dashes as
// plain ones.
func fold(s string) string {
	r := strings.NewReplacer("ё", "е", "’", "'", "‘", "'", "–", "-", "—", "-", "&", "and")
	return strings.Join(strings.Fields(r.Replace(strings.ToLower(s))), " ")
}

// translated: TMDB has the name in the language asked. It answers the
// original name when it has no translation; for Cyrillic languages the name
// must be Cyrillic (a Russian film's own name counts).
func translated(d Details, language string) bool {
	if strings.HasPrefix(language, "ru") || strings.HasPrefix(language, "uk") {
		for _, r := range d.Title {
			if unicode.Is(unicode.Cyrillic, r) {
				return true
			}
		}
		return false
	}
	return d.Title != d.OriginalTitle
}

// ParseNameQuery reads "<kind>:<year>:<tmdbId>:<title>" (year and id may be
// 0; the title may contain colons).
func ParseNameQuery(s string) (NameQuery, error) {
	parts := strings.SplitN(s, ":", 4)
	if len(parts) != 4 {
		return NameQuery{}, fmt.Errorf("item %q: want kind:year:tmdbId:title", s)
	}
	kind := Kind(parts[0])
	if kind != Movie && kind != TV {
		return NameQuery{}, fmt.Errorf("item %q: kind must be movie or tv", s)
	}
	year, err1 := strconv.Atoi(parts[1])
	id, err2 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || year < 0 || id < 0 {
		return NameQuery{}, fmt.Errorf("item %q: year and tmdbId must be numbers", s)
	}
	title := strings.TrimSpace(parts[3])
	if id == 0 && title == "" {
		return NameQuery{}, fmt.Errorf("item %q: a title or a tmdbId is needed", s)
	}
	if len(title) > 300 {
		return NameQuery{}, fmt.Errorf("item %q: title too long", s[:40])
	}
	return NameQuery{Kind: kind, TMDBID: id, Title: title, Year: year}, nil
}
