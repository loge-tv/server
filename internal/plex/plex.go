// Package plex is a minimal read-only Plex Media Server client.
package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{BaseURL: baseURL, Token: token, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

type Stream struct {
	ID           int    `json:"id"`
	StreamType   int    `json:"streamType"` // 1 video, 2 audio, 3 subtitle
	Index        *int   `json:"index"`
	Codec        string `json:"codec"`
	Channels     int    `json:"channels"`
	LanguageCode string `json:"languageCode"`
	Language     string `json:"language"`
	Default      bool   `json:"default"`
}

type Part struct {
	File     string   `json:"file"`
	Size     int64    `json:"size"`
	Duration int64    `json:"duration"` // ms
	Stream   []Stream `json:"Stream"`
}

type Media struct {
	Duration int64  `json:"duration"`
	Part     []Part `json:"Part"`
}

type Metadata struct {
	RatingKey        string  `json:"ratingKey"`
	Type             string  `json:"type"` // movie, episode
	Title            string  `json:"title"`
	GrandparentTitle string  `json:"grandparentTitle"`
	ParentRatingKey  string  `json:"parentRatingKey"`
	ParentIndex      int     `json:"parentIndex"`
	Index            int     `json:"index"`
	Duration         int64   `json:"duration"`
	UpdatedAt        int64   `json:"updatedAt"`
	Media            []Media `json:"Media"`
}

type Directory struct {
	Key   string `json:"key"`
	Type  string `json:"type"` // movie, show
	Title string `json:"title"`
}

type container struct {
	MediaContainer struct {
		Metadata  []Metadata  `json:"Metadata"`
		Directory []Directory `json:"Directory"`
	} `json:"MediaContainer"`
}

func (c *Client) get(ctx context.Context, path string, token string, q url.Values, out any) error {
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", token)
	req.Header.Set("X-Plex-Client-Identifier", "skiptv-detector")
	req.Header.Set("X-Plex-Product", "SkipTV detector")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &StatusError{Code: resp.StatusCode, Path: path}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type StatusError struct {
	Code int
	Path string
}

func (e *StatusError) Error() string { return fmt.Sprintf("plex %s: HTTP %d", e.Path, e.Code) }

// Sections lists movie and show libraries.
func (c *Client) Sections(ctx context.Context) ([]Directory, error) {
	var r container
	if err := c.get(ctx, "/library/sections", c.Token, nil, &r); err != nil {
		return nil, err
	}
	var out []Directory
	for _, d := range r.MediaContainer.Directory {
		if d.Type == "movie" || d.Type == "show" {
			out = append(out, d)
		}
	}
	return out, nil
}

// Items lists all movies (type 1) or episodes (type 4) of a section.
func (c *Client) Items(ctx context.Context, section string, typ int) ([]Metadata, error) {
	var r container
	q := url.Values{"type": {strconv.Itoa(typ)}}
	if err := c.get(ctx, "/library/sections/"+url.PathEscape(section)+"/all", c.Token, q, &r); err != nil {
		return nil, err
	}
	return r.MediaContainer.Metadata, nil
}

// Item returns full metadata of one item, including streams.
func (c *Client) Item(ctx context.Context, ratingKey string) (*Metadata, error) {
	var r container
	if err := c.get(ctx, "/library/metadata/"+url.PathEscape(ratingKey), c.Token, nil, &r); err != nil {
		return nil, err
	}
	if len(r.MediaContainer.Metadata) == 0 {
		return nil, fmt.Errorf("plex: item %s not found", ratingKey)
	}
	return &r.MediaContainer.Metadata[0], nil
}

// CheckAccess reports whether token can read this server's libraries.
// It returns (false, nil) for a rejected token and an error when Plex
// itself is unreachable.
func (c *Client) CheckAccess(ctx context.Context, token string) (bool, error) {
	err := c.get(ctx, "/library/sections", token, nil, nil)
	if err == nil {
		return true, nil
	}
	if se, ok := err.(*StatusError); ok && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden) {
		return false, nil
	}
	return false, err
}
