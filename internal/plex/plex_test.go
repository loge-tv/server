package plex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPartFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Header.Get("X-Plex-Token") != "client":
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path != "/library/metadata/55549":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"55549","Media":[
				{"Part":[{"id":1,"file":"/media/a.mkv"}]},
				{"Part":[{"id":1178507,"key":"/library/parts/1178507/1794754192/file.mkv","file":"/media/b.mkv"}]}]}]}}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "server")
	ctx := context.Background()

	if p, err := c.PartFile(ctx, "client", "55549", 1178507); err != nil || p != "/media/b.mkv" {
		t.Errorf("part of the second media: %q %v", p, err)
	}
	if _, err := c.PartFile(ctx, "client", "55549", 7); !errors.Is(err, ErrNoPart) {
		t.Errorf("unknown part: %v", err)
	}
	var se *StatusError
	if _, err := c.PartFile(ctx, "server", "55549", 1); !errors.As(err, &se) || se.Code != 401 {
		t.Errorf("asked with the server token instead of the client's: %v", err)
	}
	if _, err := c.PartFile(ctx, "client", "1", 1); !errors.As(err, &se) || se.Code != 404 {
		t.Errorf("unknown item: %v", err)
	}
}
