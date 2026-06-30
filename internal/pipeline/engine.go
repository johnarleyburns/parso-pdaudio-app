package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/johnarleyburns/parso-pdaudio/internal/cafpkg"
	"github.com/johnarleyburns/parso-pdaudio/internal/config"
	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"github.com/johnarleyburns/parso-pdaudio/internal/db"
	"github.com/johnarleyburns/parso-pdaudio/internal/ffmpeg"
	"github.com/johnarleyburns/parso-pdaudio/internal/provider"
)

const (
	StageDownload = "download"
	StageConvert  = "convert"
	StagePackage  = "package"
	StageCleaner  = "cleaner"
	StageDiscover = "discover"
)

// EnginePhase describes what the engine is currently doing.
type EnginePhase string

const (
	PhaseDiscovering EnginePhase = "discovering"
	PhaseRunning     EnginePhase = "running"
	PhaseIdle        EnginePhase = "idle-waiting"
	PhaseThrottled   EnginePhase = "throttled"
	PhaseDone        EnginePhase = "done"
)

// WorkerStatus is a snapshot of one worker's current activity.
type WorkerStatus struct {
	Stage   string
	Worker  string
	Source  string
	Title   string
	Bytes   int64
	Status  string // active | backoff | idle
	Backoff string // e.g. "429 retry 5s"
}

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
	rlim      *provider.RateLimiter

	Results chan core.ProgressMsg
	pools   map[string]*pool
	paused  atomic.Bool

	discoveredCount atomic.Int64
	discovering     atomic.Bool

	convertEnabled bool

	workerMu  sync.Mutex
	workerAct map[string]*workerActivity
	throttled atomic.Bool

	logBuf *ringBuffer
}

type workerActivity struct {
	Stage   string
	Worker  string
	Source  string
	Title   string
	Bytes   int64
	Active  bool
	Backoff string
}

// ringBuffer is a fixed-size ring buffer of log strings.
type ringBuffer struct {
	mu   sync.Mutex
	buf  []string
	pos  int
	full bool
}

func newRingBuffer(n int) *ringBuffer { return &ringBuffer{buf: make([]string, n)} }

func (rb *ringBuffer) add(s string) {
	rb.mu.Lock()
	rb.buf[rb.pos] = s
	rb.pos = (rb.pos + 1) % len(rb.buf)
	if rb.pos == 0 {
		rb.full = true
	}
	rb.mu.Unlock()
}

func (rb *ringBuffer) all() []string {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if !rb.full {
		out := make([]string, rb.pos)
		copy(out, rb.buf[:rb.pos])
		return out
	}
	out := make([]string, len(rb.buf))
	copy(out, rb.buf[rb.pos:])
	copy(out[len(rb.buf)-rb.pos:], rb.buf[:rb.pos])
	return out
}

func NewEngine(cfg *config.Config, store *db.DB, providers []provider.Provider) (*Engine, error) {
	tools := ffmpeg.Detect()
	pkg, err := cafpkg.New(cfg.Packager, tools)
	if err != nil {
		return nil, err
	}
	rlim := provider.NewRateLimiter(cfg.CommonsReqPerSec, float64(cfg.CommonsConcurrency))
	client := provider.NewClient(cfg.UserAgent)
	e := &Engine{
		cfg:            cfg,
		store:          store,
		tools:          tools,
		pkg:            pkg,
		client:         client,
		httpDL:         &http.Client{Timeout: 0},
		providers:      providers,
		rank:           provider.FormatRank(cfg.Prefer),
		rlim:           rlim,
		Results:        make(chan core.ProgressMsg, 2048),
		pools:          map[string]*pool{},
		convertEnabled: tools.Available(),
		workerAct:      map[string]*workerActivity{},
		logBuf:         newRingBuffer(256),
	}
	return e, nil
}

func (e *Engine) Tools() ffmpeg.Tools                { return e.tools }
func (e *Engine) ConvertEnabled() bool               { return e.convertEnabled }
func (e *Engine) PackagerName() string               { return e.pkg.Name() }
func (e *Engine) SetPaused(p bool)                   { e.paused.Store(p) }
func (e *Engine) Paused() bool                       { return e.paused.Load() }
func (e *Engine) SetDiscovering(v bool)              { e.discovering.Store(v) }
func (e *Engine) IsDiscovering() bool                { return e.discovering.Load() }
func (e *Engine) DiscoveredCount() int               { return int(e.discoveredCount.Load()) }
func (e *Engine) SetThrottled(v bool)                { e.throttled.Store(v) }
func (e *Engine) IsThrottled() bool                  { return e.throttled.Load() }
func (e *Engine) RateLimiter() *provider.RateLimiter { return e.rlim }

func (e *Engine) Logf(format string, args ...any) {
	e.logBuf.add(fmt.Sprintf(format, args...))
}
func (e *Engine) LogLines() []string { return e.logBuf.all() }

func (e *Engine) capReached() bool {
	return e.cfg.MaxTracks > 0 && int(e.discoveredCount.Load()) >= e.cfg.MaxTracks
}

func (e *Engine) Phase() EnginePhase {
	if e.discovering.Load() {
		return PhaseDiscovering
	}
	if e.IsThrottled() {
		return PhaseThrottled
	}
	if e.Paused() {
		return PhaseIdle
	}
	pending, _ := e.Pending(context.Background())
	if pending == 0 {
		return PhaseDone
	}
	return PhaseRunning
}

func (e *Engine) WorkerBegin(wid, stage, source, title string) {
	e.workerMu.Lock()
	e.workerAct[wid] = &workerActivity{Stage: stage, Worker: wid, Source: source, Title: title, Active: true}
	e.workerMu.Unlock()
}

func (e *Engine) WorkerUpdate(wid string, bytes int64) {
	e.workerMu.Lock()
	if a, ok := e.workerAct[wid]; ok {
		a.Bytes = bytes
	}
	e.workerMu.Unlock()
}

func (e *Engine) WorkerBackoff(wid, reason string) {
	e.workerMu.Lock()
	if a, ok := e.workerAct[wid]; ok {
		a.Backoff = reason
		a.Active = false
	}
	e.workerMu.Unlock()
	e.SetThrottled(true)
	go func() {
		time.Sleep(5 * time.Second)
		e.SetThrottled(false)
	}()
}

func (e *Engine) WorkerEnd(wid string) {
	e.workerMu.Lock()
	delete(e.workerAct, wid)
	e.workerMu.Unlock()
}

func (e *Engine) Workers() []WorkerStatus {
	e.workerMu.Lock()
	defer e.workerMu.Unlock()
	var out []WorkerStatus
	for _, a := range e.workerAct {
		st := "active"
		if a.Backoff != "" {
			st = "backoff"
		} else if !a.Active {
			st = "idle"
		}
		out = append(out, WorkerStatus{
			Stage: a.Stage, Worker: a.Worker, Source: a.Source,
			Title: a.Title, Bytes: a.Bytes, Status: st, Backoff: a.Backoff,
		})
	}
	return out
}

func (e *Engine) emit(m core.ProgressMsg) {
	select {
	case e.Results <- m:
	default:
	}
}

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

func (e *Engine) guard(step stepFunc) stepFunc {
	return func(ctx context.Context, id string) bool {
		if e.paused.Load() {
			return false
		}
		return step(ctx, id)
	}
}

func (e *Engine) Scale(stage string, delta int) {
	if p, ok := e.pools[stage]; ok {
		p.Scale(p.Size() + delta)
	}
}

func (e *Engine) PoolSize(stage string) int {
	if p, ok := e.pools[stage]; ok {
		return p.Size()
	}
	return 0
}

func (e *Engine) Stop() {
	for _, p := range e.pools {
		p.Stop()
	}
}

func (e *Engine) Pending(ctx context.Context) (int, error) {
	var n int
	err := e.store.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tracks WHERE status NOT IN ('done','failed','skipped')`).Scan(&n)
	return n, err
}

func (e *Engine) PendingByStage(ctx context.Context) (map[string]int, error) {
	rows, err := e.store.SQL().QueryContext(ctx,
		`SELECT status, COUNT(*) FROM tracks WHERE status IN ('discovered','downloading','downloaded','converting','converted','packaging','packaged','cleaning') GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		m[st] = n
	}
	return m, rows.Err()
}

func (e *Engine) RetryFailed(ctx context.Context) (int64, error) {
	return e.store.RetryFailed(ctx, e.cfg.MaxAttempts)
}

func (e *Engine) ReviveSkipped(ctx context.Context) (int64, error) {
	return e.store.ReviveSkipped(ctx)
}

func (e *Engine) Counts(ctx context.Context) (map[string]map[string]int, error) {
	return e.store.Counts(ctx)
}

func (e *Engine) failTrack(ctx context.Context, t *core.Track, stage string, err error) bool {
	_ = e.store.MarkFailed(ctx, t.ID, fmt.Sprintf("%s: %v", stage, err))
	e.emit(core.ProgressMsg{Source: t.Source, Stage: stage, TrackID: t.ID, Failed: true, Err: err.Error()})
	e.Logf("FAIL %s/%s %s: %v", t.Source, stage, t.Title, err)
	return true
}

func (e *Engine) requeueTrack(ctx context.Context, t *core.Track, cooldownSec int) {
	_ = e.store.MarkRequeued(ctx, t.ID, cooldownSec)
	e.emit(core.ProgressMsg{Source: t.Source, Stage: StageDownload, TrackID: t.ID, Err: "requeued: rate-limit"})
	e.Logf("REQUEUE %s %s (%ds cooldown)", t.Source, t.Title, cooldownSec)
}

func (e *Engine) skipTrack(ctx context.Context, t *core.Track, reason string) {
	_ = e.store.MarkSkipped(ctx, t.ID, reason)
	e.emit(core.ProgressMsg{Source: t.Source, Stage: StageDiscover, TrackID: t.ID, Skipped: true})
	e.Logf("SKIP %s %s: %s", t.Source, t.Title, reason)
}
