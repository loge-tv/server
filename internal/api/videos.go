package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/sepiatv/server/internal/tmdb"
)

// VideoFinder gives TMDB's trailers of a movie or show.
type VideoFinder interface {
	Videos(ctx context.Context, kind tmdb.Kind, id int, language string) ([]tmdb.Video, error)
}

// videos answers GET /v1/videos?item=movie:603&language=ru-RU: YouTube
// trailers, the language asked first.
func (s *Server) videos(w http.ResponseWriter, r *http.Request) {
	if s.Videos == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "tmdb is not configured", "configured": false})
		return
	}
	k, v, ok := strings.Cut(r.URL.Query().Get("item"), ":")
	id, err := strconv.Atoi(v)
	if !ok || err != nil || id <= 0 || (k != string(tmdb.Movie) && k != string(tmdb.TV)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "item must look like movie:603 or tv:1396"})
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
	videos, err := s.Videos.Videos(r.Context(), tmdb.Kind(k), id, language)
	if err != nil {
		s.Log.Warn("tmdb videos", "item", k+":"+v, "err", err)
		msg := "tmdb is unreachable"
		if errors.Is(err, tmdb.ErrBadKey) {
			msg = "tmdb refused the api key"
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": msg})
		return
	}
	if videos == nil {
		videos = []tmdb.Video{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"videos": videos})
}
