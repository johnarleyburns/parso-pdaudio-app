package db

import (
	"context"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
)

// ListForFingerprint returns done tracks with a CAF that have not yet been
// fingerprinted (used by the dedup pass). Already-marked duplicates are skipped.
func (d *DB) ListForFingerprint(ctx context.Context, force bool) ([]*core.Track, error) {
	where := `WHERE status='done' AND caf_path IS NOT NULL`
	if !force {
		where += ` AND audio_fingerprint IS NULL`
	}
	return d.queryTracks(ctx, `SELECT `+trackCols+` FROM tracks `+where+` ORDER BY created_at`)
}

// ListFingerprinted returns all done+caf tracks that have a fingerprint, for
// clustering. Excludes rows already marked as duplicates so re-runs are stable.
func (d *DB) ListFingerprinted(ctx context.Context) ([]*core.Track, error) {
	return d.queryTracks(ctx, `SELECT `+trackCols+` FROM tracks
	  WHERE status='done' AND caf_path IS NOT NULL AND audio_fingerprint IS NOT NULL
	    AND dup_of IS NULL ORDER BY created_at`)
}

// SetFingerprint stores a track's audio fingerprint.
func (d *DB) SetFingerprint(ctx context.Context, id, fp string) error {
	return d.update(ctx, `UPDATE tracks SET audio_fingerprint=? WHERE id=?`, nsv(fp), id)
}

// MarkDuplicate marks a track as a duplicate of canonicalID.
func (d *DB) MarkDuplicate(ctx context.Context, id, canonicalID string) error {
	return d.update(ctx,
		`UPDATE tracks SET status=?, dup_of=?, updated_at=? WHERE id=?`,
		core.StatusDuplicate, canonicalID, now(), id)
}

// CountUndeduped returns the number of done+caf tracks that have not been
// fingerprinted yet. `sync` uses this as a watermark to refuse uploading a DB
// that has not been through the dedup pass.
func (d *DB) CountUndeduped(ctx context.Context) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks
	  WHERE status='done' AND caf_path IS NOT NULL AND audio_fingerprint IS NULL`).Scan(&n)
	return n, err
}

// ListCanonicalCAF returns the id + relative caf path of every canonical (non
// duplicate) done track, for upload.
func (d *DB) ListCanonicalCAF(ctx context.Context) ([]*core.Track, error) {
	return d.queryTracks(ctx, `SELECT `+trackCols+` FROM tracks WHERE `+playablePred+
		` ORDER BY id`)
}
