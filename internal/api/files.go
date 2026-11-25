package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sepiatv/server/internal/plex"
)

// FileFinder finds the file of an item's part on disk, asking Plex with the
// client's token (so the token must see that item). Errors: *plex.StatusError
// from Plex, plex.ErrNoPart.
type FileFinder interface {
	PartFile(ctx context.Context, token, ratingKey string, partID int64) (string, error)
}

type fileEntry struct {
	path    string
	expires time.Time
}

// videoTypes: Content-Type by extension (Go's mime table lacks most video
// containers and would sniff MKV as WebM).
var videoTypes = map[string]string{
	".mkv": "video/x-matroska", ".mk3d": "video/x-matroska", ".mp4": "video/mp4",
	".m4v": "video/x-m4v", ".mov": "video/quicktime", ".webm": "video/webm",
	".avi": "video/x-msvideo", ".ts": "video/mp2t", ".m2ts": "video/mp2t", ".mts": "video/mp2t",
	".mpg": "video/mpeg", ".mpeg": "video/mpeg", ".wmv": "video/x-ms-wmv", ".flv": "video/x-flv",
}

// file serves the original file of a part, with Range (seeking) and HEAD.
// Outside the home network Plex without Plex Pass refuses the file itself
// (403 "Downloading requires a Remote Watch Pass or Plex Pass"); the
// server has the same library mounted and hands it out instead. The path
// always comes from Plex, never from the request.
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	if s.Files == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "file serving is off"})
		return
	}
	key := r.PathValue("ratingKey")
	partID, err := strconv.ParseInt(r.PathValue("partId"), 10, 64)
	if key == "" || err != nil || partID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad ratingKey or partId"})
		return
	}
	path, err := s.partFile(r.Context(), tokenOf(r), key, partID)
	if err != nil {
		var se *plex.StatusError
		switch {
		case errors.Is(err, plex.ErrNoPart):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such part"})
		case errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "token has no access to this item"})
		case errors.As(err, &se) && se.Code == http.StatusNotFound:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such item"})
		default:
			s.Log.Error("plex part file", "ratingKey", key, "part", partID, "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "plex is unavailable"})
		}
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.Log.Warn("open part file", "ratingKey", key, "part", partID, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "file unavailable"})
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "file unavailable"})
		return
	}
	ct, ok := videoTypes[strings.ToLower(filepath.Ext(path))]
	if !ok {
		ct = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "private, no-transform")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// partFile resolves a part through Plex, caching the answer per token for TTL:
// a player opens the file anew on every seek.
func (s *Server) partFile(ctx context.Context, token, key string, partID int64) (string, error) {
	h := sha256.Sum256([]byte(token + "\x00" + key + "\x00" + strconv.FormatInt(partID, 10)))
	now := time.Now()
	s.mu.Lock()
	e, hit := s.files[h]
	s.mu.Unlock()
	if hit && now.Before(e.expires) {
		return e.path, nil
	}
	path, err := s.Files.PartFile(ctx, token, key, partID)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("plex gave a relative path")
	}
	s.mu.Lock()
	if s.files == nil {
		s.files = map[[32]byte]fileEntry{}
	}
	for k, v := range s.files {
		if now.After(v.expires) {
			delete(s.files, k)
		}
	}
	s.files[h] = fileEntry{path: filepath.Clean(path), expires: now.Add(s.TTL)}
	s.mu.Unlock()
	return filepath.Clean(path), nil
}

// tokenOf: the X-Plex-Token header; for files also the query parameter,
// since a browser's <video src> can't send headers.
func tokenOf(r *http.Request) string {
	if t := r.Header.Get("X-Plex-Token"); t != "" {
		return t
	}
	if strings.HasPrefix(r.URL.Path, "/v1/files/") {
		return r.URL.Query().Get("X-Plex-Token")
	}
	return ""
}
