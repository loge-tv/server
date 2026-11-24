package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/loge-tv/server/internal/youtube"
)

// StreamFinder gives a stream of a YouTube video that a TV plays itself.
type StreamFinder interface {
	// Stream: someone pressed play.
	Stream(ctx context.Context, key string) (youtube.Stream, error)
	// Prefetch: a page opened and may play it; after the presses.
	Prefetch(ctx context.Context, key string) (youtube.Stream, error)
}

var youtubeKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{6,32}$`)

// stream answers GET /v1/videos/{key}/stream (a YouTube id from /v1/videos):
// {"url": HLS master playlist, "expiresAt"}. YouTube binds the playlist to
// the server's IP: it plays only from the same network. ?prefetch=1: a page
// asks before anyone pressed play; such requests wait behind the presses.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	if s.Streams == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "yt-dlp is not available", "configured": false})
		return
	}
	key := r.PathValue("key")
	if !youtubeKeyRe.MatchString(key) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key must be a YouTube video id"})
		return
	}
	find := s.Streams.Stream
	if r.URL.Query().Get("prefetch") == "1" {
		find = s.Streams.Prefetch
	}
	st, err := find(r.Context(), key)
	switch {
	case errors.Is(err, youtube.ErrNoStream):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "the video has no stream to play"})
	case err != nil:
		if r.Context().Err() == nil {
			s.Log.Warn("trailer stream", "key", key, "err", err)
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "youtube gave no stream"})
	default:
		writeJSON(w, http.StatusOK, st)
	}
}
