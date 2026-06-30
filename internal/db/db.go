// Package db owns the SQLite store that doubles as the pipeline work queue.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
)

// DB wraps the SQLite handle and the library directory.
type DB struct {
	sql *sql.DB
	dir string
}

// Schema is the full DDL applied at open time.
const Schema = `
CREATE TABLE IF NOT EXISTS tracks (
    id              TEXT PRIMARY KEY,
    source          TEXT NOT NULL,
    source_item     TEXT,
    title           TEXT,
    work            TEXT,
    movement        TEXT,
    composer        TEXT,
    performer       TEXT,
    album           TEXT,
    year            INTEGER,
    date_raw        TEXT,
    duration_sec    REAL,
    original_url    TEXT NOT NULL,
    original_format TEXT,
    original_codec  TEXT,
    original_bytes  INTEGER,
    original_sha1   TEXT,
    opus_path       TEXT,
    opus_bytes      INTEGER,
    caf_path        TEXT,
    caf_bytes       INTEGER,
    license_short   TEXT,
    license_url     TEXT,
    search_text     TEXT,
    status          TEXT NOT NULL DEFAULT 'discovered',
    worker          TEXT,
    attempts        INTEGER NOT NULL DEFAULT 0,
    stage_error     TEXT,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    UNIQUE(source, original_url)
);
CREATE INDEX IF NOT EXISTS idx_tracks_status     ON tracks(status);
CREATE INDEX IF NOT EXISTS idx_tracks_source     ON tracks(source);
CREATE INDEX IF NOT EXISTS idx_tracks_src_status ON tracks(source, status);

CREATE TABLE IF NOT EXISTS track_keyword (
    track_id  TEXT NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    keyword   TEXT NOT NULL,
    PRIMARY KEY (track_id, keyword)
);
CREATE INDEX IF NOT EXISTS idx_keyword ON track_keyword(keyword);

CREATE VIRTUAL TABLE IF NOT EXISTS tracks_fts USING fts5(
    body,
    track_id UNINDEXED,
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TABLE IF NOT EXISTS source_state (
    source        TEXT PRIMARY KEY,
    last_cursor   TEXT,
    discovered_at INTEGER,
    total_known   INTEGER
);
`

// Open opens (creating if needed) the library DB inside dir with WAL mode.
func Open(dir string) (*DB, error) {
	abs, err := filepath.Abs(filepath.Join(dir, "library.db"))
	if err != nil {
		return nil, err
	}
	dsn := "file:" + abs +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(on)"

	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sdb.SetMaxOpenConns(8)
	sdb.SetConnMaxLifetime(0)

	if _, err := sdb.ExecContext(context.Background(), Schema); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &DB{sql: sdb, dir: dir}, nil
}

// Close closes the underlying handle.
func (d *DB) Close() error { return d.sql.Close() }

// Dir returns the library directory.
func (d *DB) Dir() string { return d.dir }

// SQL exposes the raw handle (tests / advanced queries).
func (d *DB) SQL() *sql.DB { return d.sql }

func now() int64 { return time.Now().Unix() }

// ResetTransient resets rows stuck in a transient status (from a crash) back
// to their input status so the pipeline can resume cleanly.
func (d *DB) ResetTransient(ctx context.Context) (int64, error) {
	var total int64
	for from, to := range core.TransientStatuses {
		res, err := d.sql.ExecContext(ctx,
			`UPDATE tracks SET status=?, worker=NULL, updated_at=? WHERE status=?`,
			to, now(), from)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// InsertCandidate inserts a discovered track row; existing (source,url) pairs
// are left untouched so discovery is idempotent and resume-safe.
func (d *DB) InsertCandidate(ctx context.Context, t *core.Track) (inserted bool, err error) {
	ts := now()
	res, err := d.sql.ExecContext(ctx, `
INSERT INTO tracks (
  id, source, source_item, title, work, movement, composer, performer, album,
  year, date_raw, original_url, original_format, license_short, license_url,
  status, created_at, updated_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(source, original_url) DO NOTHING`,
		t.ID, t.Source, t.SourceItem, ns(t.Title), ns(t.Work), ns(t.Movement),
		ns(t.Composer), ns(t.Performer), ns(t.Album), ni(t.Year), ns(t.DateRaw),
		t.OriginalURL, ns(t.OriginalFormat), ns(t.LicenseShort), ns(t.LicenseURL),
		t.Status, ts, ts)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Claim atomically moves one row from `from` to `to` (single-statement
// UPDATE...RETURNING under the write lock) and returns it. ok=false if none.
func (d *DB) Claim(ctx context.Context, from, to, worker string) (*core.Track, bool, error) {
	row := d.sql.QueryRowContext(ctx, `
UPDATE tracks
   SET status=?, worker=?, updated_at=?
 WHERE id = (SELECT id FROM tracks WHERE status=? ORDER BY created_at LIMIT 1)
RETURNING id, source, source_item, original_url, original_format,
          original_codec, opus_path, caf_path`,
		to, worker, now(), from)

	t := &core.Track{Status: to}
	var item, codec, opus, caf sql.NullString
	err := row.Scan(&t.ID, &t.Source, &item, &t.OriginalURL, &t.OriginalFormat,
		&codec, &opus, &caf)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	t.SourceItem = item.String
	t.OriginalCodec = codec.String
	t.OpusPath = opus.String
	t.CafPath = caf.String
	return t, true, nil
}

// CountBy returns input-status row count (for parking workers).
func (d *DB) CountBy(ctx context.Context, status string) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tracks WHERE status=?`, status).Scan(&n)
	return n, err
}

// SetDownloaded records a completed download.
func (d *DB) SetDownloaded(ctx context.Context, id string, bytes int64, sha1 string) error {
	return d.update(ctx,
		`UPDATE tracks SET status=?, original_bytes=?, original_sha1=?,
		 worker=NULL, updated_at=? WHERE id=?`,
		core.StatusDownloaded, bytes, sha1, now(), id)
}

// SetConverted records a completed opus conversion.
func (d *DB) SetConverted(ctx context.Context, id, opusPath string, opusBytes int64, dur float64, codec string) error {
	return d.update(ctx,
		`UPDATE tracks SET status=?, opus_path=?, opus_bytes=?, duration_sec=?,
		 original_codec=?, worker=NULL, updated_at=? WHERE id=?`,
		core.StatusConverted, opusPath, opusBytes, dur, codec, now(), id)
}

// SetPackaged records a completed caf packaging.
func (d *DB) SetPackaged(ctx context.Context, id, cafPath string, cafBytes int64) error {
	return d.update(ctx,
		`UPDATE tracks SET status=?, caf_path=?, caf_bytes=?, worker=NULL,
		 updated_at=? WHERE id=?`,
		core.StatusPackaged, cafPath, cafBytes, now(), id)
}

// SetDone marks a track fully processed.
func (d *DB) SetDone(ctx context.Context, id string) error {
	return d.update(ctx,
		`UPDATE tracks SET status=?, worker=NULL, updated_at=? WHERE id=?`,
		core.StatusDone, now(), id)
}

// MarkFailed records a stage failure and increments attempts.
func (d *DB) MarkFailed(ctx context.Context, id, stageErr string) error {
	return d.update(ctx,
		`UPDATE tracks SET status=?, attempts=attempts+1, stage_error=?,
		 worker=NULL, updated_at=? WHERE id=?`,
		core.StatusFailed, trunc(stageErr, 500), now(), id)
}

// MarkSkipped records a track skipped (no preferred fmt / license / dedup).
func (d *DB) MarkSkipped(ctx context.Context, id, reason string) error {
	return d.update(ctx,
		`UPDATE tracks SET status=?, stage_error=?, worker=NULL, updated_at=? WHERE id=?`,
		core.StatusSkipped, trunc(reason, 500), now(), id)
}

// RetryFailed resets failed rows under the attempt ceiling to their stage
// input status. Returns the number of rows reset.
func (d *DB) RetryFailed(ctx context.Context, maxAttempts int) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `
UPDATE tracks SET status = CASE
    WHEN opus_path IS NOT NULL AND caf_path IS NOT NULL THEN 'packaged'
    WHEN opus_path IS NOT NULL THEN 'converted'
    WHEN original_sha1 IS NOT NULL THEN 'downloaded'
    ELSE 'discovered' END,
  worker=NULL, stage_error=NULL, updated_at=?
WHERE status='failed' AND attempts < ?`, now(), maxAttempts)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Index (re)writes the keyword and FTS rows for a track.
func (d *DB) Index(ctx context.Context, id string, keywords []string, searchText string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `UPDATE tracks SET search_text=? WHERE id=?`, searchText, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM track_keyword WHERE track_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tracks_fts WHERE track_id=?`, id); err != nil {
		return err
	}
	for _, kw := range keywords {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO track_keyword(track_id, keyword) VALUES (?,?)`, id, kw); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tracks_fts(body, track_id) VALUES (?,?)`, searchText, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Counts returns per-source, per-status row counts.
func (d *DB) Counts(ctx context.Context) (map[string]map[string]int, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT source, status, COUNT(*) FROM tracks GROUP BY source, status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var src, st string
		var n int
		if err := rows.Scan(&src, &st, &n); err != nil {
			return nil, err
		}
		if out[src] == nil {
			out[src] = map[string]int{}
		}
		out[src][st] = n
	}
	return out, rows.Err()
}

// SetSourceState upserts a source's pagination cursor and total estimate.
func (d *DB) SetSourceState(ctx context.Context, source, cursor string, total int) error {
	_, err := d.sql.ExecContext(ctx, `
INSERT INTO source_state(source, last_cursor, discovered_at, total_known)
VALUES (?,?,?,?)
ON CONFLICT(source) DO UPDATE SET last_cursor=excluded.last_cursor,
  discovered_at=excluded.discovered_at, total_known=excluded.total_known`,
		source, cursor, now(), total)
	return err
}

// GetSourceCursor returns the saved pagination cursor for a source.
func (d *DB) GetSourceCursor(ctx context.Context, source string) (string, error) {
	var cur sql.NullString
	err := d.sql.QueryRowContext(ctx,
		`SELECT last_cursor FROM source_state WHERE source=?`, source).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return cur.String, err
}

const trackCols = `id, source, source_item, title, work, movement, composer,
performer, album, year, date_raw, duration_sec, original_url, original_format,
original_codec, original_bytes, original_sha1, opus_path, opus_bytes, caf_path,
caf_bytes, license_short, license_url, status, attempts, stage_error`

func scanTrack(s interface{ Scan(...any) error }) (*core.Track, error) {
	t := &core.Track{}
	var item, title, work, mov, comp, perf, alb, dateRaw sql.NullString
	var origFmt, origCodec, origSHA, opus, caf, licS, licU, stageErr sql.NullString
	var year, origBytes, opusBytes, cafBytes sql.NullInt64
	var dur sql.NullFloat64
	err := s.Scan(&t.ID, &t.Source, &item, &title, &work, &mov, &comp, &perf,
		&alb, &year, &dateRaw, &dur, &t.OriginalURL, &origFmt, &origCodec,
		&origBytes, &origSHA, &opus, &opusBytes, &caf, &cafBytes, &licS, &licU,
		&t.Status, &t.Attempts, &stageErr)
	if err != nil {
		return nil, err
	}
	t.SourceItem = item.String
	t.Title, t.Work, t.Movement = title.String, work.String, mov.String
	t.Composer, t.Performer, t.Album = comp.String, perf.String, alb.String
	t.Year, t.DateRaw, t.DurationSec = int(year.Int64), dateRaw.String, dur.Float64
	t.OriginalFormat, t.OriginalCodec, t.OriginalSHA1 = origFmt.String, origCodec.String, origSHA.String
	t.OriginalBytes = origBytes.Int64
	t.OpusPath, t.OpusBytes = opus.String, opusBytes.Int64
	t.CafPath, t.CafBytes = caf.String, cafBytes.Int64
	t.LicenseShort, t.LicenseURL, t.StageError = licS.String, licU.String, stageErr.String
	return t, nil
}

// GetTrack loads a full track by id.
func (d *DB) GetTrack(ctx context.Context, id string) (*core.Track, error) {
	row := d.sql.QueryRowContext(ctx, `SELECT `+trackCols+` FROM tracks WHERE id=?`, id)
	return scanTrack(row)
}

// ListPlayable returns done tracks (with kept artifacts) ordered for display.
func (d *DB) ListPlayable(ctx context.Context, limit int) ([]*core.Track, error) {
	q := `SELECT ` + trackCols + ` FROM tracks
	      WHERE status='done' AND opus_path IS NOT NULL
	      ORDER BY source, composer, work, title LIMIT ?`
	return d.queryTracks(ctx, q, limit)
}

// ListAll returns up to limit tracks ordered by source/status (TUI list).
func (d *DB) ListAll(ctx context.Context, limit int) ([]*core.Track, error) {
	q := `SELECT ` + trackCols + ` FROM tracks ORDER BY source, created_at LIMIT ?`
	return d.queryTracks(ctx, q, limit)
}

// Search runs an FTS5 MATCH and returns matching done/playable tracks.
func (d *DB) Search(ctx context.Context, query string, playableOnly bool, limit int) ([]*core.Track, error) {
	match := buildMatch(query)
	if match == "" {
		if playableOnly {
			return d.ListPlayable(ctx, limit)
		}
		return d.ListAll(ctx, limit)
	}
	cond := ""
	if playableOnly {
		cond = ` AND t.status='done' AND t.opus_path IS NOT NULL`
	}
	q := `SELECT ` + prefixCols("t.") + `
	      FROM tracks_fts f JOIN tracks t ON t.id = f.track_id
	      WHERE f.body MATCH ?` + cond + `
	      ORDER BY rank LIMIT ?`
	return d.queryTracks(ctx, q, match, limit)
}

func (d *DB) queryTracks(ctx context.Context, q string, args ...any) ([]*core.Track, error) {
	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.Track
	for rows.Next() {
		t, err := scanTrack(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d *DB) update(ctx context.Context, q string, args ...any) error {
	_, err := d.sql.ExecContext(ctx, q, args...)
	return err
}

// buildMatch turns free user text into a safe FTS5 prefix query.
func buildMatch(q string) string {
	fields := strings.Fields(strings.ToLower(q))
	var terms []string
	for _, f := range fields {
		var b strings.Builder
		for _, r := range f {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		if b.Len() == 0 {
			continue
		}
		terms = append(terms, b.String()+"*")
	}
	return strings.Join(terms, " ")
}

func prefixCols(p string) string {
	parts := strings.Split(trackCols, ",")
	for i := range parts {
		parts[i] = p + strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ", ")
}

func ns(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func ni(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
