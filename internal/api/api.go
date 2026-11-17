// Package api serves detected segments to SkipTV clients.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/emildeev/skiptv/detector/internal/store"
)

// Checker reports whether a Plex token may use our Plex server.
type Checker interface {
	CheckAccess(ctx context.Context, token string) (bool, error)
}

type Server struct {
	Store   *store.Store
	Auth    Checker
	Log     *slog.Logger
	TTL     time.Duration // cache of accepted tokens
	DenyTTL time.Duration // cache of rejected tokens

	mu    sync.Mutex
	cache map[[32]byte]authEntry
}

type authEntry struct {
	ok      bool
	expires time.Time
}

const maxBatch = 500

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.Handle("GET /v1/segments/{ratingKey}", s.auth(http.HandlerFunc(s.one)))
	mux.Handle("GET /v1/segments", s.auth(http.HandlerFunc(s.batch)))
	mux.Handle("GET /v1/stats", s.auth(http.HandlerFunc(s.stats)))
	return mux
}

type itemJSON struct {
	RatingKey string          `json:"ratingKey"`
	Segments  []store.Segment `json:"segments"`
	Analyzed  *bool           `json:"analyzed,omitempty"`
}

func toJSON(key string, r *store.Result) itemJSON {
	segs := make([]store.Segment, len(r.Segments))
	for i, s := range r.Segments {
		s.Start, s.End = math.Round(s.Start*10)/10, math.Round(s.End*10)/10
		segs[i] = s
	}
	out := itemJSON{RatingKey: key, Segments: segs}
	if !r.Analyzed {
		f := false
		out.Analyzed = &f
	}
	return out
}

func (s *Server) one(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("ratingKey")
	res, err := s.Store.Results(r.Context(), []string{key})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toJSON(key, res[key]))
}

func (s *Server) batch(w http.ResponseWriter, r *http.Request) {
	var keys []string
	seen := map[string]bool{}
	for _, k := range strings.Split(r.URL.Query().Get("ratingKeys"), ",") {
		k = strings.TrimSpace(k)
		if k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ratingKeys is required"})
		return
	}
	if len(keys) > maxBatch {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "too many ratingKeys"})
		return
	}
	res, err := s.Store.Results(r.Context(), keys)
	if err != nil {
		s.fail(w, err)
		return
	}
	items := make([]itemJSON, len(keys))
	for i, k := range keys {
		items[i] = toJSON(k, res[k])
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.Stats(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Plex-Token")
		if token == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "X-Plex-Token header is required"})
			return
		}
		ok, err := s.allowed(r.Context(), token)
		if err != nil {
			s.Log.Error("plex auth check", "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "plex is unavailable"})
			return
		}
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "token has no access to this plex server"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) allowed(ctx context.Context, token string) (bool, error) {
	h := sha256.Sum256([]byte(token))
	now := time.Now()
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[[32]byte]authEntry{}
	}
	e, hit := s.cache[h]
	s.mu.Unlock()
	if hit && now.Before(e.expires) {
		return e.ok, nil
	}
	ok, err := s.Auth.CheckAccess(ctx, token)
	if err != nil {
		return false, err
	}
	ttl := s.TTL
	if !ok {
		ttl = s.DenyTTL
	}
	s.mu.Lock()
	for k, v := range s.cache {
		if now.After(v.expires) {
			delete(s.cache, k)
		}
	}
	s.cache[h] = authEntry{ok: ok, expires: now.Add(ttl)}
	s.mu.Unlock()
	return ok, nil
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.Log.Error("api", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
