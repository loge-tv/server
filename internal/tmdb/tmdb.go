// Package tmdb asks The Movie Database for recommendations (titles like a
// given movie or show, for the Loge "Потому что вы смотрели…" shelf) and for
// trailers.
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBase is TMDB's API v3.
const DefaultBase = "https://api.themoviedb.org/3"

var (
	// ErrUnavailable: TMDB could not be reached or answered with a server error.
	ErrUnavailable = errors.New("tmdb is unavailable")
	// ErrBadKey: TMDB refused the key (401).
	ErrBadKey = errors.New("tmdb refused the api key")
)

// Kind is TMDB's media type: "movie" or "tv".
type Kind string

const (
	Movie Kind = "movie"
	TV    Kind = "tv"
)

// Title is one recommended title. Clients find it in the Plex catalog by
// tmdb://<TMDBID>.
type Title struct {
	TMDBID        int    `json:"tmdbId"`
	Kind          Kind   `json:"type"`
	Title         string `json:"title"`
	OriginalTitle string `json:"originalTitle,omitempty"`
	ReleaseDate   string `json:"releaseDate,omitempty"`
	Year          int    `json:"year,omitempty"`
}

type Client struct {
	Base string
	// Key is a v3 API key (sent as api_key) or a v4 read access token (a JWT,
	// sent as a bearer token).
	Key  string
	HTTP *http.Client
	TTL  time.Duration // cache of answers; 0: a day

	mu         sync.Mutex
	cache      map[string]cacheEntry
	videoCache map[string]videoEntry
	names      map[string]nameEntry
}

type cacheEntry struct {
	titles  []Title
	expires time.Time
}

// New makes a client whose connections survive TMDB's answer for Russia:
// its DNS gives 127.0.0.1 to resolvers that pass a Russian client subnet
// (Google, Yandex, so routers using them), see Dialer.
func New(base, key string) *Client {
	d := &Dialer{Net: &net.Dialer{Timeout: 10 * time.Second}}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = d.DialContext
	return &Client{Base: base, Key: key, HTTP: &http.Client{Transport: tr, Timeout: 20 * time.Second}}
}

// Recommendations of TMDB for a movie or show, in TMDB's order, at most 20
// (the first page). An unknown id gives none.
func (c *Client) Recommendations(ctx context.Context, kind Kind, id int, language string) ([]Title, error) {
	if kind != Movie && kind != TV {
		return nil, fmt.Errorf("tmdb: unknown kind %q", kind)
	}
	key := fmt.Sprintf("%s:%d:%s", kind, id, language)
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.cache[key]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e.titles, nil
	}
	c.mu.Unlock()

	q := url.Values{"page": {"1"}}
	if language != "" {
		q.Set("language", language)
	}
	var body struct {
		Results []result `json:"results"`
	}
	status, err := c.get(ctx, fmt.Sprintf("/%s/%d/recommendations", kind, id), q, &body)
	if err != nil {
		return nil, err
	}
	var titles []Title
	if status != http.StatusNotFound {
		titles = make([]Title, 0, len(body.Results))
		for _, r := range body.Results {
			if t, ok := r.title(kind); ok {
				titles = append(titles, t)
			}
		}
	}
	ttl := c.TTL
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]cacheEntry{}
	}
	for k, e := range c.cache {
		if now.After(e.expires) {
			delete(c.cache, k)
		}
	}
	c.cache[key] = cacheEntry{titles: titles, expires: now.Add(ttl)}
	c.mu.Unlock()
	return titles, nil
}

type result struct {
	ID            int    `json:"id"`
	MediaType     string `json:"media_type"`
	Title         string `json:"title"`
	Name          string `json:"name"`
	OriginalTitle string `json:"original_title"`
	OriginalName  string `json:"original_name"`
	ReleaseDate   string `json:"release_date"`
	FirstAirDate  string `json:"first_air_date"`
	Adult         bool   `json:"adult"`
}

func (r result) title(asked Kind) (Title, bool) {
	kind := asked
	switch r.MediaType {
	case "movie":
		kind = Movie
	case "tv":
		kind = TV
	case "":
	default:
		return Title{}, false // people, collections
	}
	t := Title{TMDBID: r.ID, Kind: kind, Title: r.Title, OriginalTitle: r.OriginalTitle, ReleaseDate: r.ReleaseDate}
	if kind == TV {
		t.Title, t.OriginalTitle, t.ReleaseDate = r.Name, r.OriginalName, r.FirstAirDate
	}
	if r.ID == 0 || r.Adult || t.Title == "" {
		return Title{}, false
	}
	if t.OriginalTitle == t.Title {
		t.OriginalTitle = ""
	}
	if len(t.ReleaseDate) >= 4 {
		t.Year, _ = strconv.Atoi(t.ReleaseDate[:4])
	}
	return t, true
}

// get answers the status; a 404 is not an error (unknown id).
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) (int, error) {
	bearer := strings.HasPrefix(c.Key, "eyJ")
	if !bearer {
		q.Set("api_key", c.Key)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Base, "/")+path+"?"+q.Encode(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if bearer {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %s", ErrUnavailable, strings.ReplaceAll(err.Error(), c.Key, "…"))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return resp.StatusCode, ErrBadKey
	case resp.StatusCode == http.StatusNotFound:
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	case resp.StatusCode != http.StatusOK:
		return resp.StatusCode, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return resp.StatusCode, nil
}
