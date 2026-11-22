package tmdb

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Video is a trailer or teaser of a movie or show on YouTube, as TMDB lists
// it. Clients play it by Key (the YouTube video id).
type Video struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Type        string `json:"type"`     // Trailer or Teaser
	Language    string `json:"language"` // ISO 639-1, "" when TMDB has none
	Official    bool   `json:"official"`
	PublishedAt string `json:"publishedAt,omitempty"`
	Size        int    `json:"size,omitempty"` // 1080, 720…
}

type videoEntry struct {
	videos  []Video
	expires time.Time
}

// Videos gives the YouTube trailers and teasers of a movie or show, best
// first: in the language asked (ru-RU → ru), then English, then the rest;
// trailers before teasers, official ones first, newest first. An unknown id
// gives none.
func (c *Client) Videos(ctx context.Context, kind Kind, id int, language string) ([]Video, error) {
	if kind != Movie && kind != TV {
		return nil, fmt.Errorf("tmdb: unknown kind %q", kind)
	}
	lang, _, _ := strings.Cut(language, "-")
	key := fmt.Sprintf("%s:%d:%s", kind, id, lang)
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.videoCache[key]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e.videos, nil
	}
	c.mu.Unlock()

	// TMDB filters videos by `language` and adds the ones of
	// include_video_language: the language asked, English, and untagged.
	q := url.Values{}
	if language != "" {
		q.Set("language", language)
	}
	include := []string{"en", "null"}
	if lang != "" && lang != "en" {
		include = append([]string{lang}, include...)
	}
	q.Set("include_video_language", strings.Join(include, ","))
	var body struct {
		Results []struct {
			Key         string `json:"key"`
			Name        string `json:"name"`
			Site        string `json:"site"`
			Type        string `json:"type"`
			Language    string `json:"iso_639_1"`
			Official    bool   `json:"official"`
			PublishedAt string `json:"published_at"`
			Size        int    `json:"size"`
		} `json:"results"`
	}
	status, err := c.get(ctx, fmt.Sprintf("/%s/%d/videos", kind, id), q, &body)
	if err != nil {
		return nil, err
	}
	videos := []Video{}
	if status != http.StatusNotFound {
		for _, r := range body.Results {
			if r.Site != "YouTube" || r.Key == "" || (r.Type != "Trailer" && r.Type != "Teaser") {
				continue
			}
			videos = append(videos, Video{Key: r.Key, Name: r.Name, Type: r.Type, Language: r.Language,
				Official: r.Official, PublishedAt: r.PublishedAt, Size: r.Size})
		}
	}
	rank := func(v Video) int {
		switch {
		case lang != "" && v.Language == lang:
			return 0
		case v.Language == "en":
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(videos, func(i, j int) bool {
		a, b := videos[i], videos[j]
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		if a.Type != b.Type {
			return a.Type == "Trailer"
		}
		if a.Official != b.Official {
			return a.Official
		}
		return a.PublishedAt > b.PublishedAt
	})

	ttl := c.TTL
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	c.mu.Lock()
	if c.videoCache == nil {
		c.videoCache = map[string]videoEntry{}
	}
	for k, e := range c.videoCache {
		if now.After(e.expires) {
			delete(c.videoCache, k)
		}
	}
	c.videoCache[key] = videoEntry{videos: videos, expires: now.Add(ttl)}
	c.mu.Unlock()
	return videos, nil
}
