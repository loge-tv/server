// Command detector finds intros and credits in a Plex library by audio
// fingerprints and black frames and serves them over HTTP for SkipTV.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/emildeev/skiptv/detector/internal/analyzer"
	"github.com/emildeev/skiptv/detector/internal/api"
	"github.com/emildeev/skiptv/detector/internal/media"
	"github.com/emildeev/skiptv/detector/internal/plex"
	"github.com/emildeev/skiptv/detector/internal/store"
)

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

func main() {
	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	if len(os.Args) > 1 && os.Args[1] == "match" {
		if err := debugMatch(os.Args[2:]); err != nil {
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
	st, err := store.Open(env("DB_PATH", "/data/detector.db"))
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
		FF:     &media.FFmpeg{Bin: env("FFMPEG", "ffmpeg"), Nice: nice, Timeout: envDur("FFMPEG_TIMEOUT", 15*time.Minute)},
		Guard:  guard.wait,
		Log:    log,
		Filter: os.Getenv("ONLY_PATH"),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              env("LISTEN", ":8080"),
		Handler:           (&api.Server{Store: st, Auth: px, Log: log, TTL: 10 * time.Minute, DenyTTL: time.Minute}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	go loop(ctx, an, st, log, envDur("POLL_INTERVAL", 15*time.Minute), os.Getenv("ANALYZE") != "false")

	log.Info("listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
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
