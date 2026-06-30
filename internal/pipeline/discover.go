package pipeline

import (
	"context"
	"fmt"

	"github.com/oklog/ulid/v2"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"github.com/johnarleyburns/parso-pdaudio/internal/provider"
)

func (e *Engine) Discover(ctx context.Context) error {
	e.SetDiscovering(true)
	defer e.SetDiscovering(false)
	for _, p := range e.providers {
		if err := e.discoverOne(ctx, p); err != nil {
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

	rowFile := best
	if !ok && len(cand.Files) > 0 {
		rowFile = cand.Files[0]
	}
	if rowFile.URL == "" {
		return nil
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
		if t.Status == core.StatusSkipped {
			reason := ""
			if e.capReached() {
				reason = "cap: max tracks reached"
			} else if !ok {
				reason = "no preferred format"
			} else {
				reason = "license not allowed"
			}
			if reason != "" {
				_ = e.store.MarkSkipped(ctx, id, reason)
			}
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
