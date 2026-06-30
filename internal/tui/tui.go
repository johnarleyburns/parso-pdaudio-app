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

var poolStages = []string{
	pipeline.StageDownload, pipeline.StageConvert,
	pipeline.StagePackage, pipeline.StageCleaner,
}

const tabCount = 5
const (
	tabDashboard = iota
	tabTracks
	tabPlayer
	tabLog
	tabBrowse
)

// Messages.
type (
	progressMsg core.ProgressMsg
	playerMsg   player.Event
	tickMsg     time.Time
	tracksMsg   struct{ tracks []*core.Track }
	discoverMsg struct{ err error }
	workerMsg   struct{ workers []pipeline.WorkerStatus }
	pendingMsg  struct{ pending map[string]int }
	logsMsg     struct{ lines []string }

	browseSourcesMsg   struct{ nodes []db.BrowseEntry }
	browseComposersMsg struct{ nodes []db.BrowseEntry }
	browseTitlesMsg    struct{ nodes []db.BrowseEntry }
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
	order []string

	width, height int

	counts   map[string]map[string]int
	rates    map[string]*srcRate
	workers  []pipeline.WorkerStatus
	pending  map[string]int
	logLines []string

	bytesAccum int64
	mbps       float64
	lastTick   time.Time

	prog progress.Model

	// active tab
	tab int

	// tracks pane
	search       textinput.Model
	searchActive bool
	tracks       []*core.Track
	sel          int
	selID        string
	tickCount    int

	// browse pane
	browseLevel           int // 0=source, 1=composer, 2=title
	browseSel             int
	browseSelSource       string
	browseSelComposer     string
	browseSources         []db.BrowseEntry
	browseComposers       []db.BrowseEntry
	browseTitles          []db.BrowseEntry

	// controls
	focusPool int

	// player
	nowPlaying string
	nowTitle   string
	posSec     float64
	durSec     float64
	playerErr  string
	playState  string // loading | playing | paused | stopped
	statusLine string

	discovering bool
	playerOnly  bool
}

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
		pending:    map[string]int{},
		prog:       p,
		search:     ti,
		focusPool:  0,
		lastTick:   time.Now(),
		playerOnly: playerOnly,
		tab:        tabDashboard,
	}
}

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
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return progressMsg{}
		}
		return progressMsg(msg)
	}
}

func listenPlayer(ch <-chan player.Event) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return playerMsg{}
		}
		return playerMsg(msg)
	}
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
	eng := m.eng
	return func() tea.Msg {
		c, err := store.Counts(ctx)
		if err != nil {
			return countsMsg{}
		}
		p, _ := eng.PendingByStage(ctx)
		return countsMsg{counts: c, pending: p}
	}
}

type countsMsg struct {
	counts  map[string]map[string]int
	pending map[string]int
}

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

func (m Model) refreshBrowseSourcesCmd() tea.Cmd {
	ctx := m.ctx
	store := m.store
	return func() tea.Msg {
		nodes, err := store.BrowseSources(ctx)
		if err != nil {
			return browseSourcesMsg{}
		}
		return browseSourcesMsg{nodes: nodes}
	}
}

func (m Model) refreshBrowseComposersCmd(source string) tea.Cmd {
	ctx := m.ctx
	store := m.store
	return func() tea.Msg {
		nodes, err := store.BrowseComposers(ctx, source)
		if err != nil {
			return browseComposersMsg{}
		}
		return browseComposersMsg{nodes: nodes}
	}
}

func (m Model) refreshBrowseTitlesCmd(source, composer string) tea.Cmd {
	ctx := m.ctx
	store := m.store
	return func() tea.Msg {
		nodes, err := store.BrowseTitles(ctx, source, composer)
		if err != nil {
			return browseTitlesMsg{}
		}
		return browseTitlesMsg{nodes: nodes}
	}
}
