package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/johnarleyburns/parso-pdaudio/internal/cafpkg"
	"github.com/johnarleyburns/parso-pdaudio/internal/config"
	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"github.com/johnarleyburns/parso-pdaudio/internal/db"
	"github.com/johnarleyburns/parso-pdaudio/internal/ffmpeg"
	"github.com/johnarleyburns/parso-pdaudio/internal/provider"
)

// Stage names used for pools and progress messages.
const (
	StageDownload = "download"
	StageConvert  = "convert"
	StagePackage  = "package"
	StageCleaner  = "cleaner"
	StageDiscover = "discover"
)

// Engine owns the staged pipeline: providers, worker pools, and the
// worker->UI results channel.
type Engine struct {
	cfg       *config.Config
	store     *db.DB
	tools     ffmpeg.Tools
	pkg       cafpkg.Packager
	client    *provider.Client
	httpDL    *http.Client
	providers []provider.Provider
	rank      map[string]int

	Results chan core.ProgressMsg
	pools   map[string]*pool
	paused  atomic.Bool

	discoveredCount atomic.Int64

	convertEnabled bool
}

// NewEngine constructs an Engine. ffmpeg/ffprobe availability gates the
// convert/package/cleaner stages; download always runs.
func NewEngine(cfg *config.Config, store *db.DB, providers []provider.Provider) (*Engine, error) {
	tools := ffmpeg.Detect()
	pkg, err := cafpkg.New(cfg.Packager, tools)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		cfg:            cfg,
		store:          store,
		tools:          tools,
		pkg:            pkg,
		client:         provider.NewClient(cfg.UserAgent),
		httpDL:         &http.Client{Timeout: 0},
		providers:      providers,
		rank:           provider.FormatRank(cfg.Prefer),
		Results:        make(chan core.ProgressMsg, 2048),
		pools:          map[string]*pool{},
		convertEnabled: tools.Available(),
	}
	return e, nil
}

// Tools exposes detected binaries (for startup diagnostics).
func (e *Engine) Tools() ffmpeg.Tools { return e.tools }

// ConvertEnabled reports whether the ffmpeg-backed stages can run.
func (e *Engine) ConvertEnabled() bool { return e.convertEnabled }

// PackagerName returns the active caf packager name.
func (e *Engine) PackagerName() string { return e.pkg.Name() }

// emit sends a progress message without blocking the worker.
func (e *Engine) emit(m core.ProgressMsg) {
	select {
	case e.Results <- m:
	default:
	}
}

// SetPaused pauses or resumes all worker pools.
func (e *Engine) SetPaused(p bool) { e.paused.Store(p) }

// Paused reports whether the pipeline is paused.
func (e *Engine) Paused() bool { return e.paused.Load() }

// capReached reports whether the --max-tracks cap for newly discovered tracks
// has been reached this run.
func (e *Engine) capReached() bool {
	return e.cfg.MaxTracks > 0 && int(e.discoveredCount.Load()) >= e.cfg.MaxTracks
}

// StartWorkers launches all stage pools at their configured sizes.
func (e *Engine) StartWorkers(ctx context.Context) {
	e.pools[StageDownload] = newPool(ctx, StageDownload, e.guard(e.stepDownload))
	e.pools[StageDownload].Scale(e.cfg.DLWorkers)

	if e.convertEnabled {
		e.pools[StageConvert] = newPool(ctx, StageConvert, e.guard(e.stepConvert))
		e.pools[StageConvert].Scale(e.cfg.ConvWorkers)
		e.pools[StagePackage] = newPool(ctx, StagePackage, e.guard(e.stepPackage))
		e.pools[StagePackage].Scale(e.cfg.PkgWorkers)
		e.pools[StageCleaner] = newPool(ctx, StageCleaner, e.guard(e.stepCleaner))
		e.pools[StageCleaner].Scale(e.cfg.CleanWorkers)
	}
}

// guard wraps a step so it yields (no work) while paused.
func (e *Engine) guard(step stepFunc) stepFunc {
	return func(ctx context.Context, id string) bool {
		if e.paused.Load() {
			return false
		}
		return step(ctx, id)
	}
}

// Scale changes a pool's worker count by delta (clamped at >=0).
func (e *Engine) Scale(stage string, delta int) {
	p, ok := e.pools[stage]
	if !ok {
		return
	}
	p.Scale(p.Size() + delta)
}

// PoolSize returns a pool's current worker count.
func (e *Engine) PoolSize(stage string) int {
	if p, ok := e.pools[stage]; ok {
		return p.Size()
	}
	return 0
}

// Stop tears down all pools.
func (e *Engine) Stop() {
	for _, p := range e.pools {
		p.Stop()
	}
}

// Pending returns the count of rows in non-terminal statuses.
func (e *Engine) Pending(ctx context.Context) (int, error) {
	var n int
	err := e.store.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tracks WHERE status NOT IN ('done','failed','skipped')`).Scan(&n)
	return n, err
}

// RetryFailed resets eligible failed rows; returns count reset.
func (e *Engine) RetryFailed(ctx context.Context) (int64, error) {
	return e.store.RetryFailed(ctx, e.cfg.MaxAttempts)
}

// Counts proxies db.Counts for the UI/reporter.
func (e *Engine) Counts(ctx context.Context) (map[string]map[string]int, error) {
	return e.store.Counts(ctx)
}

// failTrack centralizes failure recording + emission.
func (e *Engine) failTrack(ctx context.Context, t *core.Track, stage string, err error) bool {
	_ = e.store.MarkFailed(ctx, t.ID, fmt.Sprintf("%s: %v", stage, err))
	e.emit(core.ProgressMsg{Source: t.Source, Stage: stage, TrackID: t.ID, Failed: true, Err: err.Error()})
	return true
}
