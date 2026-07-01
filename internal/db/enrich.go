package db

import (
	"context"
	"database/sql"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
)

// playablePred is the SQL predicate for a track whose CAF is present and is not
// a de-duplicated copy. Kept in one place so enrichment/browse stay consistent.
const playablePred = `status='done' AND caf_path IS NOT NULL AND dup_of IS NULL`

// ListForEnrichment returns done tracks that still need the LLM enrichment pass
// (enriched_at IS NULL), oldest first, up to limit (0 = all). CAF presence is
// not required so the pass can run before or independently of dedup.
func (d *DB) ListForEnrichment(ctx context.Context, limit int, force bool) ([]*core.Track, error) {
	where := `WHERE status='done'`
	if !force {
		where += ` AND enriched_at IS NULL`
	}
	q := `SELECT ` + trackCols + ` FROM tracks ` + where + ` ORDER BY created_at`
	if limit > 0 {
		q += ` LIMIT ?`
		return d.queryTracks(ctx, q, limit)
	}
	return d.queryTracks(ctx, q)
}

// EnrichResult carries the structured metadata produced for one track.
type EnrichResult struct {
	ComposerCanonical string
	WorkTitle         string
	Catalog           string
	MovementIndex     int
	MovementTitle     string
	DisplayTitle      string
	ComposerCorrected bool
	Confidence        float64
	Model             string
}

// ApplyEnrichment writes an enrichment result to a track. It also refreshes the
// legacy composer column when the LLM corrected the attribution, so existing
// browse/search that group by `composer` reflect the fix.
func (d *DB) ApplyEnrichment(ctx context.Context, id string, r EnrichResult) error {
	composerUpdate := ``
	args := []any{
		nsv(r.ComposerCanonical), nsv(r.WorkTitle), nsv(r.Catalog),
		niv(r.MovementIndex), nsv(r.MovementTitle), nsv(r.DisplayTitle),
		b2i(r.ComposerCorrected), nsv(r.Model), r.Confidence, now(),
	}
	if r.ComposerCorrected && r.ComposerCanonical != "" {
		composerUpdate = `, composer=?`
		args = append(args, r.ComposerCanonical)
	}
	args = append(args, id)
	return d.update(ctx, `UPDATE tracks SET
	  composer_canonical=?, work_title=?, catalog=?, movement_index=?,
	  movement_title=?, display_title=?, composer_corrected=?,
	  enrich_model=?, enrich_confidence=?, enriched_at=?`+composerUpdate+`
	 WHERE id=?`, args...)
}

// UpsertWork inserts or refreshes a work row.
func (d *DB) UpsertWork(ctx context.Context, w *core.Work) error {
	return d.update(ctx, `
INSERT INTO works(id, composer_canonical, title, catalog, sort_key, track_count, created_at)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET composer_canonical=excluded.composer_canonical,
  title=excluded.title, catalog=excluded.catalog, sort_key=excluded.sort_key,
  track_count=excluded.track_count`,
		w.ID, nsv(w.ComposerCanonical), nsv(w.Title), nsv(w.Catalog),
		nsv(w.SortKey), w.TrackCount, now())
}

// SetTrackWork links a track to a work.
func (d *DB) SetTrackWork(ctx context.Context, trackID, workID string) error {
	return d.update(ctx, `UPDATE tracks SET work_id=? WHERE id=?`, nsv(workID), trackID)
}

// RegroupWorks rebuilds the works table from enriched tracks. It clears existing
// work links, derives a work per (composer_canonical, work_title) group, and
// links member tracks. keyFn maps a track to a stable work id and sort key.
func (d *DB) RegroupWorks(ctx context.Context, keyFn func(*core.Track) (id, title, catalog, sortKey string)) (int, error) {
	tracks, err := d.queryTracks(ctx, `SELECT `+trackCols+` FROM tracks WHERE `+playablePred+
		` AND composer_canonical IS NOT NULL AND work_title IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	type agg struct {
		w     core.Work
		count int
	}
	works := map[string]*agg{}
	links := map[string]string{} // trackID -> workID
	for _, t := range tracks {
		id, title, catalog, sortKey := keyFn(t)
		if id == "" {
			continue
		}
		a := works[id]
		if a == nil {
			a = &agg{w: core.Work{ID: id, ComposerCanonical: t.ComposerCanonical, Title: title, Catalog: catalog, SortKey: sortKey}}
			works[id] = a
		}
		a.count++
		links[t.ID] = id
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM works`); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tracks SET work_id=NULL`); err != nil {
		return 0, err
	}
	for _, a := range works {
		a.w.TrackCount = a.count
		if _, err := tx.ExecContext(ctx, `
INSERT INTO works(id, composer_canonical, title, catalog, sort_key, track_count, created_at)
VALUES (?,?,?,?,?,?,?)`, a.w.ID, nsv(a.w.ComposerCanonical), nsv(a.w.Title),
			nsv(a.w.Catalog), nsv(a.w.SortKey), a.w.TrackCount, now()); err != nil {
			return 0, err
		}
	}
	for trackID, workID := range links {
		if _, err := tx.ExecContext(ctx, `UPDATE tracks SET work_id=? WHERE id=?`, workID, trackID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(works), nil
}

// BrowseWorks returns works (grouped movements) with playable counts for a
// source+composer, ordered by sort key. Tracks not yet grouped into a work fall
// back to a synthetic entry keyed by their title.
func (d *DB) BrowseWorks(ctx context.Context, source, composer string) ([]BrowseEntry, error) {
	rows, err := d.sql.QueryContext(ctx, `
SELECT COALESCE(w.title, t.work_title, t.title) AS name,
       COALESCE(t.work_id, 'title:'||t.title) AS key,
       COALESCE(w.sort_key, t.work_title, t.title) AS sortk,
       COUNT(*) AS n
  FROM tracks t
  LEFT JOIN works w ON w.id = t.work_id
 WHERE t.`+playablePred+` AND t.source=? AND (t.composer IS NOT DISTINCT FROM ?)
 GROUP BY key
 ORDER BY sortk COLLATE NOCASE`, source, composer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BrowseEntry
	for rows.Next() {
		var name, key, sortk sql.NullString
		var e BrowseEntry
		if err := rows.Scan(&name, &key, &sortk, &e.Count); err != nil {
			return nil, err
		}
		e.Key = key.String
		e.Name = name.String
		if e.Name == "" {
			e.Name = "—"
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// WorkTracks returns the playable tracks of a work (its movements) ordered by
// movement index. The key is either a works.id or a "title:<title>" synthetic
// key emitted by BrowseWorks for ungrouped tracks.
func (d *DB) WorkTracks(ctx context.Context, key string, limit int) ([]*core.Track, error) {
	order := ` ORDER BY movement_index, title COLLATE NOCASE LIMIT ?`
	if title, ok := stripPrefix(key, "title:"); ok {
		q := `SELECT ` + trackCols + ` FROM tracks WHERE ` + playablePred +
			` AND title IS NOT DISTINCT FROM ?` + order
		return d.queryTracks(ctx, q, title, limit)
	}
	q := `SELECT ` + trackCols + ` FROM tracks WHERE ` + playablePred +
		` AND work_id=?` + order
	return d.queryTracks(ctx, q, key, limit)
}

func stripPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return "", false
}

func nsv(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func niv(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
