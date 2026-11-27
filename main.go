// Command sepia-server (Sepia Server) finds intros and credits in a Plex library
// by audio fingerprints and black frames and serves them over HTTP to Sepia TV clients.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sepiatv/server/internal/analyzer"
	"github.com/sepiatv/server/internal/api"
	"github.com/sepiatv/server/internal/discovery"
	"github.com/sepiatv/server/internal/media"
	"github.com/sepiatv/server/internal/plex"
	"github.com/sepiatv/server/internal/store"
	"github.com/sepiatv/server/internal/tmdb"
	"github.com/sepiatv/server/internal/trailers"
	"github.com/sepiatv/server/internal/youtube"
)

// version is set at build time (-ldflags -X main.version=…): the release tag
// without "v", or main-<sha>.
var version = "dev"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func logLevel() slog.Level {
	if os.Getenv("LOG_LEVEL") == "debug" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()}))
	debug := map[string]func([]string) error{"match": debugMatch, "segments": debugSegments, "credits": debugCredits, "frames": debugFrames, "scan": debugScan}
	if cmd, ok := debug[os.Args[min(1, len(os.Args)-1)]]; ok && len(os.Args) > 1 {
		if err := cmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	token := os.Getenv("PLEX_TOKEN")
	if token == "" {
		return errors.New("PLEX_TOKEN is required")
	}
	px := plex.New(env("PLEX_URL", "http://127.0.0.1:32400"), token)
	st, err := store.Open(env("DB_PATH", "/data/sepia-server.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	nice, _ := strconv.Atoi(env("FFMPEG_NICE", "15"))
	minMem, _ := strconv.ParseInt(env("MIN_MEM_AVAILABLE_MB", "400"), 10, 64)
	guard := &memGuard{
		vmURL: os.Getenv("VM_URL"), query: env("VM_MEM_QUERY", `node_memory_MemAvailable_bytes{node="raspberrypi5"}`),
		min: minMem << 20, log: log, http: &http.Client{Timeout: 10 * time.Second},
	}
	an := &analyzer.Analyzer{
		Cfg:    analyzer.DefaultConfig(),
		Plex:   px,
		Store:  st,
		FF:     &media.FFmpeg{Bin: env("FFMPEG", "ffmpeg"), Probe: env("FFPROBE", "ffprobe"), Nice: nice, Timeout: envDur("FFMPEG_TIMEOUT", 15*time.Minute)},
		Guard:  guard.wait,
		Log:    log,
		Filter: os.Getenv("ONLY_PATH"),
	}

	// TMDB is optional: without a key /v1/recommendations and /v1/videos answer 404.
	var recs api.Recommender
	var videos api.VideoFinder
	var names api.Namer
	if key := os.Getenv("TMDB_API_KEY"); key != "" {
		c := tmdb.New(env("TMDB_URL", tmdb.DefaultBase), key)
		recs, videos, names = c, c, c
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Trailers played on TVs: optional; without yt-dlp /v1/videos/{key}/stream
	// answers 404 and apps open YouTube instead. YTDLP=off turns it off.
	var streams api.StreamFinder
	var search trailers.Searcher
	if bin := env("YTDLP", "yt-dlp"); bin != "off" {
		if _, err := exec.LookPath(bin); err == nil {
			dir := filepath.Join(os.TempDir(), "yt-dlp")
			yt := &youtube.Resolver{Bin: youtube.Updatable(bin, dir), Log: log}
			// yt-dlp kept loaded in python3 (it is a zipapp): ~2 s less per trailer
			// on a Raspberry Pi. YTDLP_WORKER=false runs the program every time.
			if py, err := exec.LookPath("python3"); err == nil && os.Getenv("YTDLP_WORKER") != "false" {
				yt.Worker = &youtube.Worker{Python: py, Zipapp: yt.Bin, Dir: dir, Log: log}
			}
			streams, search = yt, yt
			if os.Getenv("YTDLP_UPDATE") != "false" {
				go yt.UpdateLoop(ctx, 24*time.Hour) // loads the worker after the first update
			} else if yt.Worker != nil {
				go yt.Worker.Warm()
			}
		} else {
			log.Info("no yt-dlp: TVs open trailers in YouTube", "bin", bin)
		}
	}

	// TMDB lists few dubbed trailers: without one in the language asked, YouTube's
	// search is asked for one (TRAILER_SEARCH=false: TMDB's only).
	if c, ok := videos.(*tmdb.Client); ok && search != nil && os.Getenv("TRAILER_SEARCH") != "false" {
		videos = &trailers.Finder{TMDB: c, YouTube: search, Log: log}
	}

	listen := env("LISTEN", ":8080")
	var ident atomic.Pointer[plex.Identity]
	go identify(ctx, px, &ident, log, listen)

	srv := &http.Server{
		Addr: listen,
		Handler: (&api.Server{Store: st, Auth: px, Files: px, Recs: recs, Videos: videos, Names: names, Streams: streams, Log: log, TTL: 10 * time.Minute, DenyTTL: time.Minute,
			Version: version, PublicURL: os.Getenv("PUBLIC_URL"), MinimalRoot: os.Getenv("ROOT_PAGE") == "minimal",
			Plex: func() api.Plex {
				if id := ident.Load(); id != nil {
					return api.Plex{MachineIdentifier: id.MachineIdentifier, Name: id.FriendlyName}
				}
				return api.Plex{}
			}}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	go loop(ctx, an, st, log, envDur("POLL_INTERVAL", 15*time.Minute), os.Getenv("ANALYZE") != "false")

	log.Info("listening", "addr", srv.Addr, "version", version)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// identify learns which Plex server this is (for /v1/info and the root page),
// retrying until Plex answers, then announces the server on the local network
// (MDNS=off: no) and keeps the name fresh. The announcement names the port of
// LISTEN, or MDNS_PORT when the server is reached on another one. It is a
// convenience: when it can't start (no multicast in a container's own network,
// port 5353 taken), the server goes on without it.
func identify(ctx context.Context, px *plex.Client, ident *atomic.Pointer[plex.Identity], log *slog.Logger, listen string) {
	wait := 5 * time.Second
	announced := false
	for {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		id, err := px.Identity(c)
		cancel()
		if err == nil {
			ident.Store(&id)
			wait = time.Hour
			if !announced && os.Getenv("MDNS") != "off" {
				announced = true
				port, _ := strconv.Atoi(os.Getenv("MDNS_PORT"))
				if port == 0 {
					if _, p, err := net.SplitHostPort(listen); err == nil {
						port, _ = strconv.Atoi(p)
					}
				}
				if stop, err := discovery.Announce(id.FriendlyName, id.MachineIdentifier, version, port); err != nil {
					log.Info("not announced on the local network", "err", err)
				} else {
					log.Info("announced on the local network", "service", discovery.Service, "port", port)
					defer stop()
				}
			}
		} else if ident.Load() == nil {
			log.Warn("plex identity", "err", err)
			wait = min(wait*2, time.Minute)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func loop(ctx context.Context, an *analyzer.Analyzer, st *store.Store, log *slog.Logger, every time.Duration, analyze bool) {
	for {
		t0 := time.Now()
		if err := an.Sync(ctx); err != nil {
			log.Error("plex sync", "err", err)
		} else if analyze {
			if err := an.Work(ctx); err != nil && ctx.Err() == nil {
				log.Error("analysis", "err", err)
			}
			if s, err := st.Stats(ctx); err == nil {
				log.Info("pass done", "took", time.Since(t0).Round(time.Second), "stats", fmt.Sprintf("%+v", s))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// memGuard blocks analysis while the node has less available memory than
// min, according to VictoriaMetrics. Without VM_URL it never blocks; if VM
// can't be queried it lets work go on (the pod's own memory limit still holds).
type memGuard struct {
	vmURL, query string
	min          int64
	log          *slog.Logger
	http         *http.Client
}

func (g *memGuard) wait(ctx context.Context) error {
	if g.vmURL == "" {
		return ctx.Err()
	}
	warned := false
	for {
		avail, err := g.available(ctx)
		if err != nil {
			g.log.Warn("memory check", "err", err)
			return ctx.Err()
		}
		if avail >= g.min {
			if warned {
				g.log.Info("memory recovered, resuming", "availableMB", avail>>20)
			}
			return nil
		}
		if !warned {
			g.log.Warn("node low on memory, pausing analysis", "availableMB", avail>>20, "minMB", g.min>>20)
			warned = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Minute):
		}
	}
}

func (g *memGuard) available(ctx context.Context) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.vmURL+"/api/v1/query?query="+url.QueryEscape(g.query), nil)
	if err != nil {
		return 0, err
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var r struct {
		Data struct {
			Result []struct {
				Value [2]any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return 0, err
	}
	if len(r.Data.Result) == 0 {
		return 0, errors.New("no memory metric")
	}
	s, _ := r.Data.Result[0].Value[1].(string)
	v, err := strconv.ParseFloat(s, 64)
	return int64(v), err
}
