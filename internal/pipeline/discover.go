package pipeline

import (
	"context"
	"fmt"

	"github.com/oklog/ulid/v2"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"github.com/johnarleyburns/parso-pdaudio/internal/provider"
)

// Discover runs discovery for every configured provider, selecting the best
// format per candidate and inserting tracks. It is idempotent and resumable.
func (e *Engine) Discover(ctx context.Context) error {
	for _, p := range e.providers {
		if err := e.discoverOne(ctx, p); err != nil {
			// Per the spec, an empty/zero source must not abort the run. Log via
			// a failed progress message but keep going.
			e.emit(core.ProgressMsg{Source: p.Key(), Stage: StageDiscover, Failed: true, Err: err.Error()})
		}
	}
	return nil
}

func (e *Engine) discoverOne(ctx context.Context, p provider.Provider) error {
	cursor, err := e.store.GetSourceCursor(ctx, p.Key())
	if err != nil {
		return err
	}
	for {
		cands, next, done, err := p.Discover(ctx, cursor)
		if err != nil {
			return fmt.Errorf("discover %s: %w", p.Key(), err)
		}
		for i := range cands {
			if err := e.ingest(ctx, p.Key(), &cands[i]); err != nil {
				return err
			}
		}
		if err := e.store.SetSourceState(ctx, p.Key(), next, 0); err != nil {
			return err
		}
		if done || e.capReached() {
			return nil
		}
		cursor = next
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

func (e *Engine) ingest(ctx context.Context, source string, cand *core.Candidate) error {
	best, ok := provider.SelectBest(cand.Files, e.rank, e.cfg.AllowFallback)

	// Determine the URL/format for the row (chosen, else first variant).
	rowFile := best
	if !ok && len(cand.Files) > 0 {
		rowFile = cand.Files[0]
	}
	if rowFile.URL == "" {
		return nil // nothing usable
	}

	id := ulid.Make().String()
	t := &core.Track{
		ID:             id,
		Source:         source,
		SourceItem:     cand.SourceItem,
		Title:          cand.Meta.Title,
		Work:           cand.Meta.Work,
		Movement:       cand.Meta.Movement,
		Composer:       cand.Meta.Composer,
		Performer:      cand.Meta.Performer,
		Album:          cand.Meta.Album,
		Year:           cand.Meta.Year,
		DateRaw:        cand.Meta.DateRaw,
		OriginalURL:    rowFile.URL,
		OriginalFormat: rowFile.Format,
		OriginalBytes:  rowFile.Bytes,
		LicenseShort:   cand.Meta.LicenseShort,
		LicenseURL:     cand.Meta.LicenseURL,
		Status:         core.StatusDiscovered,
	}

	switch {
	case !ok:
		t.Status = core.StatusSkipped
	case !e.cfg.LicenseAllowed(cand.Meta.LicenseShort, cand.Meta.LicenseURL):
		t.Status = core.StatusSkipped
	case e.capReached():
		t.Status = core.StatusSkipped
	}

	inserted, err := e.store.InsertCandidate(ctx, t)
	if err != nil {
		return err
	}
	if inserted {
		if t.Status == core.StatusDiscovered {
			e.discoveredCount.Add(1)
		}
		e.emit(core.ProgressMsg{
			Source:  source,
			Stage:   StageDiscover,
			TrackID: id,
			Skipped: t.Status == core.StatusSkipped,
		})
	}
	return nil
}
