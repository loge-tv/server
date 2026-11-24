package tmdb

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Details is what a trailer search on YouTube needs of a title: its name in
// the language asked (TMDB gives the original one without a translation) and
// its years — a show's from the first season to the last.
type Details struct {
	Title         string
	OriginalTitle string
	Year          int // 0: unknown
	LastYear      int // a show's last season; a movie's Year
}

// Details of a movie or show; ok is false for an unknown id.
func (c *Client) Details(ctx context.Context, kind Kind, id int, language string) (Details, bool, error) {
	if kind != Movie && kind != TV {
		return Details{}, false, fmt.Errorf("tmdb: unknown kind %q", kind)
	}
	q := url.Values{}
	if language != "" {
		q.Set("language", language)
	}
	var body struct {
		Title         string `json:"title"`
		Name          string `json:"name"`
		OriginalTitle string `json:"original_title"`
		OriginalName  string `json:"original_name"`
		ReleaseDate   string `json:"release_date"`
		FirstAirDate  string `json:"first_air_date"`
		LastAirDate   string `json:"last_air_date"`
	}
	status, err := c.get(ctx, fmt.Sprintf("/%s/%d", kind, id), q, &body)
	if err != nil {
		return Details{}, false, err
	}
	if status == http.StatusNotFound {
		return Details{}, false, nil
	}
	d := Details{Title: body.Title, OriginalTitle: body.OriginalTitle, Year: year(body.ReleaseDate)}
	d.LastYear = d.Year
	if kind == TV {
		d.Title, d.OriginalTitle, d.Year = body.Name, body.OriginalName, year(body.FirstAirDate)
		d.LastYear = max(d.Year, year(body.LastAirDate))
	}
	return d, d.Title != "", nil
}

// Search finds movies and shows by name (TMDB's first page), names in the
// language asked.
func (c *Client) Search(ctx context.Context, query, language string) ([]Title, error) {
	q := url.Values{"query": {query}, "page": {"1"}}
	if language != "" {
		q.Set("language", language)
	}
	var body struct {
		Results []result `json:"results"`
	}
	if _, err := c.get(ctx, "/search/multi", q, &body); err != nil {
		return nil, err
	}
	titles := []Title{}
	for _, r := range body.Results {
		if r.MediaType != "movie" && r.MediaType != "tv" {
			continue
		}
		if t, ok := r.title(Movie); ok {
			titles = append(titles, t)
		}
	}
	return titles, nil
}

func year(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(date[:4])
	return y
}
