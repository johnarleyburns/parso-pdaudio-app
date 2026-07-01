package enrich

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"github.com/johnarleyburns/parso-pdaudio/internal/db"
	"golang.org/x/text/unicode/norm"
)

// Options configure a RunEnrich pass.
type Options struct {
	APIKey        string
	Model         string
	Limit         int
	Force         bool
	Concurrency   int
	EscalateBelow float64 // escalate to the opus model below this confidence
	Progress      func(done, total int)
}

// RunEnrich enriches un-enriched done tracks via the LLM, writes structured
// fields + a precomputed display title, then rebuilds the works grouping.
func RunEnrich(ctx context.Context, store *db.DB, opt Options) error {
	if opt.Concurrency <= 0 {
		opt.Concurrency = 4
	}
	if opt.EscalateBelow == 0 {
		opt.EscalateBelow = 0.55
	}
	tracks, err := store.ListForEnrichment(ctx, opt.Limit, opt.Force)
	if err != nil {
		return err
	}
	total := len(tracks)
	if total == 0 {
		fmt.Println("enrich: nothing to do")
		return regroup(ctx, store)
	}

	bulk := NewClient(opt.APIKey, opt.Model)
	escalate := NewClient(opt.APIKey, EscalationModel)

	jobs := make(chan *core.Track)
	var done int64
	var wg sync.WaitGroup
	for i := 0; i < opt.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				if err := enrichOne(ctx, store, bulk, escalate, t, opt.EscalateBelow); err != nil {
					fmt.Printf("enrich: %s: %v\n", t.ID, err)
				}
				n := atomic.AddInt64(&done, 1)
				if opt.Progress != nil {
					opt.Progress(int(n), total)
				}
			}
		}()
	}
	for _, t := range tracks {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		case jobs <- t:
		}
	}
	close(jobs)
	wg.Wait()

	return regroup(ctx, store)
}

func enrichOne(ctx context.Context, store *db.DB, bulk, escalate *Client, t *core.Track, escalateBelow float64) error {
	in := Input{
		Title:      t.Title,
		Source:     t.Source,
		SourceItem: t.SourceItem,
		Composer:   t.Composer,
		Performer:  t.Performer,
		Album:      t.Album,
	}
	res, err := callWithRetry(ctx, bulk, in)
	if err != nil {
		return err
	}
	// Escalate low-confidence rows (and low-confidence composer corrections) to
	// the stronger model before trusting the result.
	if res.Confidence < escalateBelow || (res.ComposerCorrected && res.Confidence < 0.75) {
		if er, eerr := callWithRetry(ctx, escalate, in); eerr == nil {
			res = er
		}
	}

	display := computeDisplay(t, res)
	return store.ApplyEnrichment(ctx, t.ID, db.EnrichResult{
		ComposerCanonical: res.ComposerCanonical,
		WorkTitle:         res.WorkTitle,
		Catalog:           res.Catalog,
		MovementIndex:     res.MovementIndex,
		MovementTitle:     res.MovementTitle,
		DisplayTitle:      display,
		ComposerCorrected: res.ComposerCorrected,
		Confidence:        res.Confidence,
		Model:             res.Model,
	})
}

func callWithRetry(ctx context.Context, c *Client, in Input) (Result, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Result{}, ctx.Err()
			case <-time.After(time.Duration(1<<uint(attempt-1)) * time.Second):
			}
		}
		r, err := c.Enrich(ctx, in)
		if err == nil {
			return r, nil
		}
		lastErr = err
		if !IsRetryable(err) {
			return Result{}, err
		}
	}
	return Result{}, lastErr
}

// computeDisplay builds the global-context display string using core.DisplayTitle
// so the Go TUI, the player, and the Swift port all agree.
func computeDisplay(t *core.Track, r Result) string {
	tmp := &core.Track{
		Source:            t.Source,
		Title:             t.Title,
		Work:              t.Work,
		SourceItem:        t.SourceItem,
		ID:                t.ID,
		Composer:          t.Composer,
		ComposerCanonical: r.ComposerCanonical,
		WorkTitle:         r.WorkTitle,
		Catalog:           r.Catalog,
		MovementTitle:     r.MovementTitle,
	}
	return core.DisplayTitle(tmp, core.DisplayGlobal)
}

func regroup(ctx context.Context, store *db.DB) error {
	n, err := store.RegroupWorks(ctx, workKey)
	if err != nil {
		return fmt.Errorf("regroup works: %w", err)
	}
	fmt.Printf("enrich: grouped tracks into %d works\n", n)
	return nil
}

// workKey maps a track to a stable work id + display/sort fields. Movements of
// the same work share (composer_canonical, work_title[, catalog]).
func workKey(t *core.Track) (id, title, catalog, sortKey string) {
	composer := normalizeKey(t.ComposerCanonical)
	title = t.WorkTitle
	catalog = t.Catalog
	workNorm := normalizeKey(t.WorkTitle)
	if composer == "" || workNorm == "" {
		return "", "", "", ""
	}
	cat := normalizeKey(t.Catalog)
	raw := composer + "|" + workNorm
	if cat != "" {
		raw += "|" + cat
	}
	sum := sha1.Sum([]byte(raw))
	id = hex.EncodeToString(sum[:])
	sortKey = workNorm
	return id, title, catalog, sortKey
}

func normalizeKey(s string) string {
	s = norm.NFKD.String(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
