package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"github.com/johnarleyburns/parso-pdaudio/internal/db"
	"github.com/johnarleyburns/parso-pdaudio/internal/player"
	"github.com/johnarleyburns/parso-pdaudio/internal/r2"
)

type mode int

const (
	modeBrowse mode = iota
	modeSearch
)

// browse levels: 0 source, 1 composer, 2 work, 3 track
type model struct {
	store  *db.DB
	play   *player.Player
	client *r2.Client
	cache  string

	ctx    context.Context
	width  int
	height int

	mode   mode
	search textinput.Model

	// browse state
	level     int
	sel       int
	selSource string
	selComp   string
	selWork   string
	nodes     []db.BrowseEntry
	tracks    []*core.Track // level 3 or search results

	status  string
	nowPlay string
	err     string
}

func newModel(store *db.DB, pl *player.Player, client *r2.Client, cache string) model {
	ti := textinput.New()
	ti.Placeholder = "search…"
	ti.Prompt = "/ "
	m := model{
		store: store, play: pl, client: client, cache: cache,
		ctx: context.Background(), search: ti, mode: modeBrowse,
	}
	return m
}

func (m model) Init() tea.Cmd {
	return m.loadSources()
}

// ---- messages ----

type nodesMsg struct {
	nodes []db.BrowseEntry
	level int
}
type tracksMsg struct{ tracks []*core.Track }
type playedMsg struct {
	title string
	err   string
}
type errMsg struct{ err string }

// ---- commands ----

func (m model) loadSources() tea.Cmd {
	return func() tea.Msg {
		n, err := m.store.BrowseSources(m.ctx)
		if err != nil {
			return errMsg{err.Error()}
		}
		return nodesMsg{nodes: n, level: 0}
	}
}

func (m model) loadComposers(source string) tea.Cmd {
	return func() tea.Msg {
		n, err := m.store.BrowseComposers(m.ctx, source)
		if err != nil {
			return errMsg{err.Error()}
		}
		return nodesMsg{nodes: n, level: 1}
	}
}

func (m model) loadWorks(source, composer string) tea.Cmd {
	return func() tea.Msg {
		n, err := m.store.BrowseWorks(m.ctx, source, composer)
		if err != nil {
			return errMsg{err.Error()}
		}
		return nodesMsg{nodes: n, level: 2}
	}
}

func (m model) loadWorkTracks(workKey string) tea.Cmd {
	return func() tea.Msg {
		ts, err := m.store.WorkTracks(m.ctx, workKey, 500)
		if err != nil {
			return errMsg{err.Error()}
		}
		return tracksMsg{tracks: ts}
	}
}

func (m model) runSearch(q string) tea.Cmd {
	return func() tea.Msg {
		ts, err := m.store.Search(m.ctx, q, true, 200)
		if err != nil {
			return errMsg{err.Error()}
		}
		return tracksMsg{tracks: ts}
	}
}

// playTrack fetches the CAF from R2 (cached) and hands the local path to the player.
func (m model) playTrack(t *core.Track) tea.Cmd {
	client := m.client
	cache := m.cache
	pl := m.play
	ctx := m.ctx
	return func() tea.Msg {
		if t.CafPath == "" {
			return playedMsg{err: "track has no CAF"}
		}
		local := filepath.Join(cache, t.ID+".caf")
		if _, err := os.Stat(local); err != nil {
			if client == nil {
				return playedMsg{err: "no R2 client for streaming"}
			}
			if err := client.GetToFile(ctx, "audio/"+t.ID+".caf", local); err != nil {
				return playedMsg{err: "fetch: " + err.Error()}
			}
		}
		if pl == nil || !pl.Available() {
			return playedMsg{err: "no audio backend"}
		}
		pl.Play(local)
		return playedMsg{title: core.DisplayTitle(t, core.DisplayGlobal)}
	}
}

// ---- update ----

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case nodesMsg:
		m.nodes = msg.nodes
		m.level = msg.level
		m.tracks = nil
		if m.sel >= len(m.nodes) {
			m.sel = 0
		}
		return m, nil
	case tracksMsg:
		m.tracks = msg.tracks
		if m.mode == modeBrowse {
			m.level = 3
		}
		if m.sel >= len(m.tracks) {
			m.sel = 0
		}
		return m, nil
	case playedMsg:
		if msg.err != "" {
			m.err = msg.err
			m.status = "playback error: " + msg.err
		} else {
			m.nowPlay = msg.title
			m.status = "playing: " + msg.title
			m.err = ""
		}
		return m, nil
	case errMsg:
		m.err = msg.err
		return m, nil
	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mode == modeSearch && m.search.Focused() {
		switch msg.String() {
		case "enter":
			m.search.Blur()
			return m, m.runSearch(m.search.Value())
		case "esc":
			m.search.Blur()
			m.mode = modeBrowse
			return m, m.loadSources()
		default:
			var cmd tea.Cmd
			m.search, cmd = m.search.Update(msg)
			return m, cmd
		}
	}

	switch msg.String() {
	case "q", "ctrl+c":
		m.play.Stop()
		return m, tea.Quit
	case "/":
		m.mode = modeSearch
		m.sel = 0
		m.search.Focus()
		return m, nil
	case "esc":
		if m.mode == modeSearch {
			m.mode = modeBrowse
			return m, m.loadSources()
		}
	case "up", "ctrl+p", "k":
		if m.sel > 0 {
			m.sel--
		}
	case "down", "ctrl+n", "j":
		if m.sel < m.rowCount()-1 {
			m.sel++
		}
	case "g", "home":
		m.sel = 0
	case "G", "end":
		m.sel = m.rowCount() - 1
	case " ", "space":
		if m.play.Playing() {
			m.play.Pause()
			m.status = "paused"
		} else if m.play.Current() != "" {
			m.play.Resume()
			m.status = "playing"
		}
		return m, nil
	case "left":
		m.play.Seek(-10)
	case "right":
		m.play.Seek(10)
	case "enter":
		return m.onEnter()
	case "backspace":
		return m.onBack()
	}
	return m, nil
}

func (m model) onEnter() (tea.Model, tea.Cmd) {
	// Search results / track level → play.
	if m.mode == modeSearch || m.level == 3 {
		if m.sel >= 0 && m.sel < len(m.tracks) {
			return m, m.playTrack(m.tracks[m.sel])
		}
		return m, nil
	}
	if m.sel < 0 || m.sel >= len(m.nodes) {
		return m, nil
	}
	sel := m.nodes[m.sel]
	switch m.level {
	case 0:
		m.selSource = sel.Key
		m.sel = 0
		return m, m.loadComposers(sel.Key)
	case 1:
		m.selComp = sel.Key
		m.sel = 0
		return m, m.loadWorks(m.selSource, sel.Key)
	case 2:
		m.selWork = sel.Key
		m.sel = 0
		return m, m.loadWorkTracks(sel.Key)
	}
	return m, nil
}

func (m model) onBack() (tea.Model, tea.Cmd) {
	if m.mode == modeSearch {
		m.mode = modeBrowse
		return m, m.loadSources()
	}
	switch m.level {
	case 1:
		m.sel = 0
		return m, m.loadSources()
	case 2:
		m.sel = 0
		return m, m.loadComposers(m.selSource)
	case 3:
		m.sel = 0
		return m, m.loadWorks(m.selSource, m.selComp)
	}
	return m, nil
}

func (m model) rowCount() int {
	if m.mode == modeSearch || m.level == 3 {
		return len(m.tracks)
	}
	return len(m.nodes)
}

// ---- view ----

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	selStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)

func (m model) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("parso-player"))
	b.WriteString("  " + dimStyle.Render(m.breadcrumb()) + "\n\n")

	if m.mode == modeSearch {
		b.WriteString(m.search.View() + "\n\n")
	}

	rows := m.visibleRows()
	max := m.height - 8
	if max < 3 {
		max = 3
	}
	start := 0
	if m.sel >= max {
		start = m.sel - max + 1
	}
	for i := start; i < len(rows) && i < start+max; i++ {
		cursor := "  "
		line := rows[i]
		if i == m.sel {
			cursor = "▶ "
			line = selStyle.Render(line)
		}
		b.WriteString(cursor + line + "\n")
	}
	if len(rows) == 0 {
		b.WriteString(dimStyle.Render("  (empty)\n"))
	}

	b.WriteString("\n")
	if m.nowPlay != "" {
		pos, dur := m.play.Position()
		b.WriteString(dimStyle.Render(fmt.Sprintf("♪ %s  %s/%s\n", m.nowPlay, fmtDur(pos), fmtDur(dur))))
	}
	if m.status != "" {
		b.WriteString(dimStyle.Render(m.status) + "\n")
	}
	b.WriteString(dimStyle.Render("/ search · enter open/play · space pause · ←/→ seek · backspace up · q quit"))
	return b.String()
}

func (m model) breadcrumb() string {
	if m.mode == modeSearch {
		return "search"
	}
	parts := []string{"sources"}
	if m.level >= 1 {
		parts = append(parts, m.selSource)
	}
	if m.level >= 2 {
		parts = append(parts, orDash(m.selComp))
	}
	if m.level >= 3 {
		parts = append(parts, "work")
	}
	return strings.Join(parts, " › ")
}

func (m model) visibleRows() []string {
	if m.mode == modeSearch || m.level == 3 {
		rows := make([]string, len(m.tracks))
		ctx := core.DisplayGlobal
		if m.level == 3 {
			ctx = core.DisplayWork
		}
		for i, t := range m.tracks {
			rows[i] = core.DisplayTitle(t, ctx)
		}
		return rows
	}
	rows := make([]string, len(m.nodes))
	for i, n := range m.nodes {
		rows[i] = fmt.Sprintf("%-50s %s", trunc(n.Name, 50), dimStyle.Render(fmt.Sprintf("(%d)", n.Count)))
	}
	return rows
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func fmtDur(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	s := int(sec)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
