// Package store keeps the library snapshot, cached fingerprints and black
// frame scans, and the resulting segments in SQLite.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"

	"github.com/emildeev/skiptv/detector/internal/media"
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS items (
	rating_key  TEXT PRIMARY KEY,
	kind        TEXT NOT NULL,          -- movie | episode
	season_key  TEXT NOT NULL DEFAULT '',
	show_title  TEXT NOT NULL DEFAULT '',
	title       TEXT NOT NULL DEFAULT '',
	season      INTEGER NOT NULL DEFAULT 0,
	episode     INTEGER NOT NULL DEFAULT 0,
	path        TEXT NOT NULL,
	size        INTEGER NOT NULL,
	duration    REAL NOT NULL,          -- seconds
	audio       TEXT NOT NULL DEFAULT '', -- ffmpeg -map spec, '' = not resolved yet
	analyzed    INTEGER NOT NULL DEFAULT 0,
	attempts    INTEGER NOT NULL DEFAULT 0,
	error       TEXT NOT NULL DEFAULT '',
	analyzed_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS items_season ON items(season_key);
CREATE TABLE IF NOT EXISTS fingerprints (
	rating_key TEXT NOT NULL,
	part       TEXT NOT NULL,           -- intro | credits
	offset     REAL NOT NULL,           -- seconds of the first point in the file
	data       BLOB NOT NULL,
	PRIMARY KEY (rating_key, part)
);
CREATE TABLE IF NOT EXISTS black_scans (
	rating_key TEXT PRIMARY KEY,
	start      REAL NOT NULL,
	frames     TEXT NOT NULL            -- JSON []media.Frame
);
CREATE TABLE IF NOT EXISTS segments (
	rating_key TEXT NOT NULL,
	type       TEXT NOT NULL,
	start      REAL NOT NULL,
	end        REAL NOT NULL,
	source     TEXT NOT NULL,           -- fingerprint | blackframe
	PRIMARY KEY (rating_key, type)
);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type Item struct {
	RatingKey  string
	Kind       string
	SeasonKey  string
	ShowTitle  string
	Title      string
	Season     int
	Episode    int
	Path       string
	Size       int64
	Duration   float64
	Audio      string
	Analyzed   bool
	Attempts   int
	Error      string
	AnalyzedAt int64
}

const itemCols = `rating_key, kind, season_key, show_title, title, season, episode, path, size, duration, audio, analyzed, attempts, error, analyzed_at`

func scanItem(sc interface{ Scan(...any) error }) (Item, error) {
	var it Item
	err := sc.Scan(&it.RatingKey, &it.Kind, &it.SeasonKey, &it.ShowTitle, &it.Title, &it.Season, &it.Episode,
		&it.Path, &it.Size, &it.Duration, &it.Audio, &it.Analyzed, &it.Attempts, &it.Error, &it.AnalyzedAt)
	return it, err
}

func (s *Store) Items(ctx context.Context) (map[string]Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemCols+` FROM items`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out[it.RatingKey] = it
	}
	return out, rows.Err()
}

func (s *Store) Item(ctx context.Context, key string) (Item, bool, error) {
	it, err := scanItem(s.db.QueryRowContext(ctx, `SELECT `+itemCols+` FROM items WHERE rating_key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, false, nil
	}
	return it, err == nil, err
}

// UpsertItem stores library metadata. If the file changed (path or size),
// cached analysis of the item is dropped.
func (s *Store) UpsertItem(ctx context.Context, it Item) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var path string
	var size int64
	err = tx.QueryRowContext(ctx, `SELECT path, size FROM items WHERE rating_key=?`, it.RatingKey).Scan(&path, &size)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO items (rating_key, kind, season_key, show_title, title, season, episode, path, size, duration, audio)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`, it.RatingKey, it.Kind, it.SeasonKey, it.ShowTitle, it.Title, it.Season, it.Episode,
			it.Path, it.Size, it.Duration, it.Audio)
	case err != nil:
	case path != it.Path || size != it.Size:
		for _, q := range []string{`DELETE FROM fingerprints WHERE rating_key=?`, `DELETE FROM black_scans WHERE rating_key=?`, `DELETE FROM segments WHERE rating_key=?`} {
			if _, err = tx.ExecContext(ctx, q, it.RatingKey); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE items SET kind=?, season_key=?, show_title=?, title=?, season=?, episode=?, path=?, size=?, duration=?,
			audio=?, analyzed=0, attempts=0, error='', analyzed_at=0 WHERE rating_key=?`,
			it.Kind, it.SeasonKey, it.ShowTitle, it.Title, it.Season, it.Episode, it.Path, it.Size, it.Duration, it.Audio, it.RatingKey)
	default:
		_, err = tx.ExecContext(ctx, `UPDATE items SET kind=?, season_key=?, show_title=?, title=?, season=?, episode=?, duration=? WHERE rating_key=?`,
			it.Kind, it.SeasonKey, it.ShowTitle, it.Title, it.Season, it.Episode, it.Duration, it.RatingKey)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetAudio(ctx context.Context, key, spec string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE items SET audio=? WHERE rating_key=?`, spec, key)
	return err
}

// DeleteItems removes items that are no longer in Plex.
func (s *Store) DeleteItems(ctx context.Context, keys []string) error {
	for _, k := range keys {
		for _, q := range []string{`DELETE FROM items WHERE rating_key=?`, `DELETE FROM fingerprints WHERE rating_key=?`,
			`DELETE FROM black_scans WHERE rating_key=?`, `DELETE FROM segments WHERE rating_key=?`} {
			if _, err := s.db.ExecContext(ctx, q, k); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) Fail(ctx context.Context, key string, cause error) error {
	msg := cause.Error()
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE items SET attempts=attempts+1, error=? WHERE rating_key=?`, msg, key)
	return err
}

type Fingerprint struct {
	Offset float64
	Data   []byte
}

func (s *Store) PutFingerprint(ctx context.Context, key, part string, fp Fingerprint) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO fingerprints (rating_key, part, offset, data) VALUES (?,?,?,?)`,
		key, part, fp.Offset, fp.Data)
	return err
}

func (s *Store) Fingerprint(ctx context.Context, key, part string) (Fingerprint, bool, error) {
	var fp Fingerprint
	err := s.db.QueryRowContext(ctx, `SELECT offset, data FROM fingerprints WHERE rating_key=? AND part=?`, key, part).Scan(&fp.Offset, &fp.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return fp, false, nil
	}
	return fp, err == nil, err
}

func (s *Store) PutBlackScan(ctx context.Context, key string, start float64, frames []media.Frame) error {
	b, err := json.Marshal(frames)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR REPLACE INTO black_scans (rating_key, start, frames) VALUES (?,?,?)`, key, start, string(b))
	return err
}

func (s *Store) BlackScan(ctx context.Context, key string) ([]media.Frame, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT frames FROM black_scans WHERE rating_key=?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var frames []media.Frame
	err = json.Unmarshal([]byte(raw), &frames)
	return frames, err == nil, err
}

type Segment struct {
	Type   string  `json:"type"`
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	Source string  `json:"-"`
}

// SetResult replaces the segments of an item and marks it analyzed.
func (s *Store) SetResult(ctx context.Context, key string, segs []Segment) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM segments WHERE rating_key=?`, key); err != nil {
		return err
	}
	for _, sg := range segs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO segments (rating_key, type, start, end, source) VALUES (?,?,?,?,?)`,
			key, sg.Type, sg.Start, sg.End, sg.Source); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE items SET analyzed=1, error='', analyzed_at=? WHERE rating_key=?`, time.Now().Unix(), key); err != nil {
		return err
	}
	return tx.Commit()
}

// Result is what the API returns for one item.
type Result struct {
	Known    bool
	Analyzed bool
	Segments []Segment
}

func (s *Store) Results(ctx context.Context, keys []string) (map[string]*Result, error) {
	out := make(map[string]*Result, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
		out[k] = &Result{Segments: []Segment{}}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT rating_key, analyzed FROM items WHERE rating_key IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var a bool
		if err := rows.Scan(&k, &a); err != nil {
			rows.Close()
			return nil, err
		}
		out[k].Known, out[k].Analyzed = true, a
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT rating_key, type, start, end, source FROM segments WHERE rating_key IN (`+ph+`) ORDER BY rating_key, start`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var sg Segment
		if err := rows.Scan(&k, &sg.Type, &sg.Start, &sg.End, &sg.Source); err != nil {
			return nil, err
		}
		out[k].Segments = append(out[k].Segments, sg)
	}
	return out, rows.Err()
}

// Stats summarises progress for /v1/stats and logs.
type Stats struct {
	Movies           int `json:"movies"`
	MoviesAnalyzed   int `json:"moviesAnalyzed"`
	MoviesCredits    int `json:"moviesWithCredits"`
	Episodes         int `json:"episodes"`
	EpisodesAnalyzed int `json:"episodesAnalyzed"`
	EpisodesIntro    int `json:"episodesWithIntro"`
	EpisodesCredits  int `json:"episodesWithCredits"`
	CreditsByBlack   int `json:"creditsFromBlackFrames"`
	Failed           int `json:"failed"`
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM items WHERE kind='movie'),
		(SELECT count(*) FROM items WHERE kind='movie' AND analyzed=1),
		(SELECT count(*) FROM segments s JOIN items i USING(rating_key) WHERE i.kind='movie' AND s.type='credits'),
		(SELECT count(*) FROM items WHERE kind='episode'),
		(SELECT count(*) FROM items WHERE kind='episode' AND analyzed=1),
		(SELECT count(*) FROM segments s JOIN items i USING(rating_key) WHERE i.kind='episode' AND s.type='intro'),
		(SELECT count(*) FROM segments s JOIN items i USING(rating_key) WHERE i.kind='episode' AND s.type='credits'),
		(SELECT count(*) FROM segments WHERE type='credits' AND source='blackframe'),
		(SELECT count(*) FROM items WHERE analyzed=0 AND attempts>0)`).Scan(
		&st.Movies, &st.MoviesAnalyzed, &st.MoviesCredits, &st.Episodes, &st.EpisodesAnalyzed,
		&st.EpisodesIntro, &st.EpisodesCredits, &st.CreditsByBlack, &st.Failed)
	return st, err
}
