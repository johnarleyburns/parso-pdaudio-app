// Package tui implements the Bubble Tea dashboard + built-in player.
package tui

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/johnarleyburns/parso-pdaudio/internal/config"
	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"github.com/johnarleyburns/parso-pdaudio/internal/db"
	"github.com/johnarleyburns/parso-pdaudio/internal/pipeline"
	"github.com/johnarleyburns/parso-pdaudio/internal/player"
)

// stages tracked in the per-source dashboard.
var poolStages = []string{
	pipeline.StageDownload, pipeline.StageConvert,
	pipeline.StagePackage, pipeline.StageCleaner,
}

// Messages.
type (
	progressMsg core.ProgressMsg
	playerMsg   player.Event
	tickMsg     time.Time
	tracksMsg   struct{ tracks []*core.Track }
	discoverMsg struct{ err error }
)

type srcRate struct {
	ewma     float64
	lastDone int
}

// Model is the root Bubble Tea model.
type Model struct {
	ctx   context.Context
	cfg   *config.Config
	eng   *pipeline.Engine
	store *db.DB
	play  *player.Player
	order []string // source keys in display order

	width, height int

	counts map[string]map[string]int
	rates  map[string]*srcRate

	bytesAccum int64
	mbps       float64
	lastTick   time.Time

	prog progress.Model

	// tracks pane
	search       textinput.Model
	searchActive bool
	tracks       []*core.Track
	sel          int
	selID        string
	tickCount    int

	// controls
	focusPool int // index into poolStages

	nowPlaying string // track id
	statusLine string

	discovering bool
	playerOnly  bool
}

// New builds the root model.
func New(ctx context.Context, cfg *config.Config, eng *pipeline.Engine, store *db.DB, pl *player.Player, order []string, playerOnly bool) Model {
	ti := textinput.New()
	ti.Placeholder = "search (FTS): chopin ballade"
	ti.Prompt = "/ "
	ti.CharLimit = 120

	p := progress.New(progress.WithDefaultGradient())

	return Model{
		ctx:        ctx,
		cfg:        cfg,
		eng:        eng,
		store:      store,
		play:       pl,
		order:      order,
		counts:     map[string]map[string]int{},
		rates:      map[string]*srcRate{},
		prog:       p,
		search:     ti,
		focusPool:  0,
		lastTick:   time.Now(),
		playerOnly: playerOnly,
	}
}

// Init starts background discovery and the listener/tick loops.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		listenResults(m.eng.Results),
		listenPlayer(m.play.Events()),
		tickCmd(),
		m.reconcileCmd(),
		m.refreshTracksCmd(""),
	}
	if !m.playerOnly {
		cmds = append(cmds, m.discoverCmd())
	}
	return tea.Batch(cmds...)
}

// --- commands ---

func listenResults(ch <-chan core.ProgressMsg) tea.Cmd {
	return func() tea.Msg { return progressMsg(<-ch) }
}

func listenPlayer(ch <-chan player.Event) tea.Cmd {
	return func() tea.Msg { return playerMsg(<-ch) }
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) discoverCmd() tea.Cmd {
	return func() tea.Msg {
		err := m.eng.Discover(m.ctx)
		return discoverMsg{err: err}
	}
}

func (m Model) reconcileCmd() tea.Cmd {
	ctx := m.ctx
	store := m.store
	return func() tea.Msg {
		c, err := store.Counts(ctx)
		if err != nil {
			return tracksMsg{} // ignore; will retry next tick
		}
		return countsMsg{counts: c}
	}
}

type countsMsg struct{ counts map[string]map[string]int }

func (m Model) refreshTracksCmd(query string) tea.Cmd {
	ctx := m.ctx
	store := m.store
	return func() tea.Msg {
		ts, err := store.Search(ctx, query, false, 1000)
		if err != nil {
			return tracksMsg{}
		}
		return tracksMsg{tracks: ts}
	}
}
