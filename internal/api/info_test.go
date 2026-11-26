package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func infoServer(t *testing.T, s *Server) *httptest.Server {
	t.Helper()
	if s.Log == nil {
		s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s.Auth = &fakeAuth{}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestInfoNeedsNoTokenAndNamesThePlexServer(t *testing.T) {
	known := false
	srv := infoServer(t, &Server{Version: "0.7.0", Plex: func() Plex {
		if known {
			return Plex{MachineIdentifier: "abc123", Name: "Home"}
		}
		return Plex{}
	}})
	code, body := get(t, srv.URL+"/v1/info", "")
	if code != 200 || strings.TrimSpace(body) != `{"product":"Sepia Server","version":"0.7.0"}` {
		t.Fatalf("before Plex answered: %d %s", code, body)
	}
	known = true
	code, body = get(t, srv.URL+"/v1/info", "")
	if code != 200 || strings.TrimSpace(body) != `{"product":"Sepia Server","version":"0.7.0","machineIdentifier":"abc123"}` {
		t.Fatalf("%d %s", code, body)
	}
	resp, err := http.Get(srv.URL + "/v1/info")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("no CORS on /v1/info")
	}
}

func TestUnauthorizedSaysWhy(t *testing.T) {
	srv := infoServer(t, &Server{})
	for token, want := range map[string]string{"": "token_missing", "bad": "token_rejected"} {
		code, body := get(t, srv.URL+"/v1/stats", token)
		var e struct{ Error, Code string }
		json.Unmarshal([]byte(body), &e)
		if code != 401 || e.Code != want || e.Error == "" {
			t.Fatalf("token %q: %d %s", token, code, body)
		}
	}
}

func TestRootPage(t *testing.T) {
	plex := func() Plex { return Plex{MachineIdentifier: "abc123", Name: `Dad's <NAS>`} }
	page := func(s *Server, hdr map[string]string) (int, string) {
		srv := infoServer(t, s)
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
		req.Host = "nas.example:32420"
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	has := func(body string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Fatalf("no %q in\n%s", w, body)
			}
		}
	}

	code, body := page(&Server{Version: "0.7.0", Plex: plex}, nil)
	if code != 200 {
		t.Fatal(code)
	}
	has(body, "Sepia Server is running", "Version 0.7.0", "Dad&#39;s &lt;NAS&gt;", "<svg",
		`href="https://sepiatv.com/connect#url=http%3A%2F%2Fnas.example%3A32420"`,
		`href="sepiatv://server?url=http%3A%2F%2Fnas.example%3A32420"`)

	_, body = page(&Server{Plex: plex}, map[string]string{"X-Forwarded-Proto": "https"})
	has(body, "url=https%3A%2F%2Fnas.example%3A32420")

	_, body = page(&Server{Plex: plex, PublicURL: "https://sepia.example/"}, nil)
	has(body, `url=https%3A%2F%2Fsepia.example"`)

	_, body = page(&Server{Version: "0.7.0", Plex: plex, MinimalRoot: true}, nil)
	has(body, "Sepia Server is running", "Version 0.7.0")
	for _, no := range []string{"NAS", "<svg", "sepiatv://", "nas.example"} {
		if strings.Contains(body, no) {
			t.Fatalf("minimal page has %q", no)
		}
	}

	_, body = page(&Server{}, nil)
	has(body, "Plex has not answered yet")

	srv := infoServer(t, &Server{})
	if code, _ := get(t, srv.URL+"/nope", ""); code != 404 {
		t.Fatalf("/nope: %d", code)
	}
}

func TestAccessLogAtDebugOnlyAndWithoutSecrets(t *testing.T) {
	var buf bytes.Buffer
	srv := infoServer(t, &Server{Log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	get(t, srv.URL+"/v1/stats?secret=1", "")
	get(t, srv.URL+"/v1/stats", "bad-token")
	get(t, srv.URL+"/v1/files/1/2?X-Plex-Token=query-token", "")
	out := buf.String()
	for _, w := range []string{"msg=request method=GET path=/v1/stats status=401 token=false", "path=/v1/stats status=401 token=true", "path=/v1/files/1/2 status=401 token=true"} {
		if !strings.Contains(out, w) {
			t.Fatalf("no %q in\n%s", w, out)
		}
	}
	for _, no := range []string{"bad-token", "query-token", "secret"} {
		if strings.Contains(out, no) {
			t.Fatalf("log has %q:\n%s", no, out)
		}
	}

	buf.Reset()
	quiet := infoServer(t, &Server{Log: slog.New(slog.NewTextHandler(&buf, nil))})
	get(t, quiet.URL+"/v1/info", "")
	if buf.Len() != 0 {
		t.Fatalf("logged at info: %s", buf.String())
	}
}

func TestPrivateNetworkPreflight(t *testing.T) {
	srv := infoServer(t, &Server{})
	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/v1/info", nil)
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Private-Network") != "true" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
}
