package api

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rsc.io/qr"
)

// Product is how clients recognise this server in /v1/info.
const Product = "Sepia Server"

// connectURL is the page the QR code opens: apps take it as an app link, and
// the page offers the sepiatv:// link otherwise. The server's address rides in
// the fragment, which browsers don't send.
const connectURL = "https://sepiatv.com/connect"

// Plex is what the server knows about its Plex server; empty until Plex has
// answered once.
type Plex struct {
	MachineIdentifier string
	Name              string
}

type infoJSON struct {
	Product           string `json:"product"`
	Version           string `json:"version"`
	MachineIdentifier string `json:"machineIdentifier,omitempty"`
}

func (s *Server) plex() Plex {
	if s.Plex == nil {
		return Plex{}
	}
	return s.Plex()
}

// info needs no token: apps ask it of hosts they are only guessing at, to tell
// that this is a Sepia Server and whose Plex it serves.
func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, infoJSON{Product: Product, Version: s.Version, MachineIdentifier: s.plex().MachineIdentifier})
}

// publicURL is the address apps should use: PUBLIC_URL, or the one this
// request came to.
func (s *Server) publicURL(r *http.Request) string {
	if s.PublicURL != "" {
		return strings.TrimRight(s.PublicURL, "/")
	}
	scheme := "http"
	if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
		scheme = p
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// qrSVG draws text as a QR code, one path of unit squares.
func qrSVG(text string) (template.HTML, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 4
	var b strings.Builder
	n := code.Size + 2*quiet
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" role="img" aria-label="QR code"><rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n, n, n)
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return template.HTML(b.String()), nil
}

var rootPage = template.Must(template.New("root").Parse(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Sepia Server</title>
<style>
body { font: 16px/1.5 system-ui, sans-serif; margin: 0; background: #f4efe6; color: #2b2118; }
main { max-width: 26rem; margin: 0 auto; padding: 3rem 1.25rem; }
h1 { font-size: 1.5rem; margin: 0 0 .25rem; }
p { margin: .5rem 0; }
.muted { color: #6b5d4f; font-size: .875rem; }
.qr { display: block; width: 14rem; max-width: 100%; margin: 1.5rem 0 1rem; border-radius: .5rem; }
a { color: #7a4a1d; }
code { word-break: break-all; }
@media (prefers-color-scheme: dark) {
  body { background: #1d1712; color: #efe6d8; }
  .muted { color: #a8998a; }
  a { color: #d9a56c; }
}
</style>
<main>
<h1>Sepia Server is running</h1>
<p class="muted">Version {{.Version}}{{if .Name}} · Plex server “{{.Name}}”{{end}}</p>
{{if .Waiting}}<p>Plex has not answered yet. If this stays, check <code>PLEX_URL</code> and <code>PLEX_TOKEN</code>.</p>{{end}}
{{if .Connect}}
<p>To connect the Sepia TV app, scan the code with your phone, or open the link on a device with the app.</p>
<a href="{{.Connect}}">{{.QR}}</a>
<p><a href="{{.App}}">Open in Sepia TV</a></p>
<p class="muted">Server address for the app's settings: <code>{{.URL}}</code></p>
{{end}}
</main>
</html>
`))

// root is for a person who opened the server's address in a browser: it shows
// that the server is up and how to connect an app. No secrets; MinimalRoot
// leaves out the Plex server's name and the links.
func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	p := s.plex()
	data := struct {
		Version, Name, URL string
		Waiting            bool
		Connect            template.URL
		App                template.URL
		QR                 template.HTML
	}{Version: s.Version, Waiting: p.MachineIdentifier == ""}
	if !s.MinimalRoot {
		data.Name = p.Name
		data.URL = s.publicURL(r)
		connect := connectURL + "#url=" + url.QueryEscape(data.URL)
		if svg, err := qrSVG(connect); err == nil {
			data.QR = svg
			data.Connect = template.URL(connect)
			data.App = template.URL("sepiatv://server?url=" + url.QueryEscape(data.URL))
		} else {
			s.Log.Error("qr code", "err", err)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	rootPage.Execute(w, data)
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ServeContent and http.ResponseController reach the real writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// accessLog logs every request at debug level: enough to see whether an app
// reaches the server and whether it sends a token. Never the token or the query.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.Log.Enabled(r.Context(), slog.LevelDebug) {
			next.ServeHTTP(w, r)
			return
		}
		t0 := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.code == 0 {
			sw.code = http.StatusOK
		}
		s.Log.Debug("request", "method", r.Method, "path", r.URL.Path, "status", sw.code,
			"token", tokenOf(r) != "", "took", time.Since(t0).Round(time.Millisecond))
	})
}
