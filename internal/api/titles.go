package api

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/loge-tv/server/internal/tmdb"
)

// Namer gives a catalog title's name in a language (TMDB's translation).
type Namer interface {
	LocalName(ctx context.Context, q tmdb.NameQuery, language string) (tmdb.Name, error)
}

const (
	maxNameItems   = 60
	nameConcurrent = 4
)

type nameJSON struct {
	TMDBID int    `json:"tmdbId,omitempty"`
	Title  string `json:"title,omitempty"`
	// Failed: TMDB didn't answer for this one (ask again later).
	Failed bool `json:"failed,omitempty"`
}

// titles answers GET /v1/titles?language=ru-RU&item=movie:2026:0:Soulm8te&item=tv:0:70523:Dark
// — names of catalog titles Plex has only in English, in the order asked;
// `{}` where TMDB has no translation or the title isn't found for sure.
func (s *Server) titles(w http.ResponseWriter, r *http.Request) {
	if s.Names == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "tmdb is not configured", "configured": false})
		return
	}
	language := r.URL.Query().Get("language")
	if language == "" {
		language = "ru-RU"
	}
	if !languageRe.MatchString(language) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "language must look like ru-RU"})
		return
	}
	raw := r.URL.Query()["item"]
	if len(raw) == 0 || len(raw) > maxNameItems {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "1 to 60 item parameters are needed"})
		return
	}
	queries := make([]tmdb.NameQuery, len(raw))
	for i, item := range raw {
		q, err := tmdb.ParseNameQuery(item)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		queries[i] = q
	}

	items := make([]nameJSON, len(queries))
	errs := make([]error, len(queries))
	slots := make(chan struct{}, nameConcurrent)
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			name, err := s.Names.LocalName(r.Context(), q, language)
			if err != nil {
				errs[i] = err
				items[i].Failed = true
				return
			}
			items[i] = nameJSON{TMDBID: name.TMDBID, Title: name.Title}
		}()
	}
	wg.Wait()

	failed := 0
	for _, err := range errs {
		if err == nil {
			continue
		}
		failed++
		if errors.Is(err, tmdb.ErrBadKey) {
			s.Log.Error("tmdb", "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "tmdb refused the api key"})
			return
		}
	}
	if failed > 0 {
		s.Log.Warn("tmdb names", "failed", failed, "of", len(queries), "err", errors.Join(errs...))
	}
	if failed == len(queries) {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "tmdb is unreachable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
