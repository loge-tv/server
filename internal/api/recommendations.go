package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/loge-tv/detector/internal/tmdb"
)

// Recommender gives TMDB's recommendations for a movie or show.
type Recommender interface {
	Recommendations(ctx context.Context, kind tmdb.Kind, id int, language string) ([]tmdb.Title, error)
}

const maxSeeds = 5

var languageRe = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)

type seed struct {
	kind tmdb.Kind
	id   int
}

type seedJSON struct {
	Seed   string       `json:"seed"`
	Titles []tmdb.Title `json:"titles"`
	// Failed: TMDB didn't answer for this seed (others may have).
	Failed bool `json:"failed,omitempty"`
}

// recommendations answers GET /v1/recommendations?seeds=movie:603,tv:1396&language=ru-RU.
func (s *Server) recommendations(w http.ResponseWriter, r *http.Request) {
	if s.Recs == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "tmdb is not configured", "configured": false})
		return
	}
	seeds, err := parseSeeds(r.URL.Query().Get("seeds"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
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

	items := make([]seedJSON, len(seeds))
	errs := make([]error, len(seeds))
	var wg sync.WaitGroup
	for i, sd := range seeds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items[i].Seed = string(sd.kind) + ":" + strconv.Itoa(sd.id)
			titles, err := s.Recs.Recommendations(r.Context(), sd.kind, sd.id, language)
			if err != nil {
				errs[i] = err
				items[i].Failed = true
				titles = nil
			}
			if titles == nil {
				titles = []tmdb.Title{}
			}
			items[i].Titles = titles
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
		s.Log.Warn("tmdb", "failed", failed, "of", len(seeds), "err", errors.Join(errs...))
	}
	if failed == len(seeds) {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "tmdb is unreachable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func parseSeeds(raw string) ([]seed, error) {
	var seeds []seed
	seen := map[seed]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, ":")
		id, err := strconv.Atoi(v)
		if !ok || err != nil || id <= 0 || (k != string(tmdb.Movie) && k != string(tmdb.TV)) {
			return nil, errors.New("seeds must look like movie:603,tv:1396")
		}
		sd := seed{kind: tmdb.Kind(k), id: id}
		if !seen[sd] {
			seen[sd] = true
			seeds = append(seeds, sd)
		}
	}
	if len(seeds) == 0 {
		return nil, errors.New("seeds is required")
	}
	if len(seeds) > maxSeeds {
		return nil, errors.New("too many seeds")
	}
	return seeds, nil
}
