package tui

import (
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"github.com/johnarleyburns/parso-pdaudio/internal/db"
	"github.com/johnarleyburns/parso-pdaudio/internal/pipeline"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.prog.Width = clamp(msg.Width-30, 10, 60)
		return m, nil

	case progressMsg:
		if msg.BytesDelta > 0 {
			m.bytesAccum += msg.BytesDelta
		}
		return m, listenResults(m.eng.Results)

	case playerMsg:
		m.posSec = msg.PosSec
		m.durSec = msg.DurSec
		m.playState = msg.State
		if msg.Err != "" {
			m.playerErr = msg.Err
			m.statusLine = "playback error: " + msg.Err
		}
		if msg.Ended && m.nowPlaying != "" {
			m.nowPlaying = ""
			m.nowTitle = ""
			m.playerErr = ""
			if msg.Err != "" {
				m.statusLine = "playback error: " + msg.Err
			} else {
				m.statusLine = "playback finished"
			}
		}
		return m, listenPlayer(m.play.Events())

	case countsMsg:
		m.counts = msg.counts
		m.pending = msg.pending
		return m, nil

	case tracksMsg:
		m.tracks = msg.tracks
		m.restoreSelection()
		return m, nil

	case discoverMsg:
		m.discovering = false
		if msg.err != nil {
			m.statusLine = "discovery error: " + msg.err.Error()
		} else {
			m.statusLine = "discovery complete"
		}
		return m, m.refreshTracksCmd(m.search.Value())

	case browseSourcesMsg:
		m.browseSources = msg.nodes
		if m.browseSel >= len(msg.nodes) {
			m.browseSel = max(0, len(msg.nodes)-1)
		}
		return m, nil

	case browseComposersMsg:
		m.browseComposers = msg.nodes
		if m.browseSel >= len(msg.nodes) {
			m.browseSel = max(0, len(msg.nodes)-1)
		}
		return m, nil

	case browseTitlesMsg:
		m.browseTitles = msg.nodes
		if m.browseSel >= len(msg.nodes) {
			m.browseSel = max(0, len(msg.nodes)-1)
		}
		return m, nil

	case browseTracksMsg:
		m.browseTracks = msg.tracks
		if m.browseSel >= len(msg.tracks) {
			m.browseSel = max(0, len(msg.tracks)-1)
		}
		return m, nil

	case tickMsg:
		return m.onTick(time.Time(msg))

	case tea.KeyMsg:
		return m.onKey(msg)
	}

	return m, nil
}

func (m Model) onTick(t time.Time) (tea.Model, tea.Cmd) {
	dt := t.Sub(m.lastTick).Seconds()
	if dt <= 0 {
		dt = 1
	}
	m.lastTick = t
	m.mbps = (m.mbps * 0.5) + (float64(m.bytesAccum) / dt / (1024 * 1024) * 0.5)
	m.bytesAccum = 0
	m.updateRates(dt)
	m.tickCount++
	m.workers = m.eng.Workers()
	m.logLines = m.eng.LogLines()

	// Poll player position while a track is playing.
	if m.nowPlaying != "" && m.play != nil {
		pos, dur := m.play.Position()
		m.posSec = pos
		m.durSec = dur
	}

	cmds := []tea.Cmd{tickCmd(), m.reconcileCmd()}
	if m.tickCount%2 == 0 {
		if m.tab == tabBrowse {
			cmds = append(cmds, m.refreshBrowseCurrentCmd())
		}
		cmds = append(cmds, m.refreshTracksCmd(m.search.Value()))
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) updateRates(dt float64) {
	const alpha = 0.2
	for _, src := range m.order {
		done := m.counts[src][core.StatusDone]
		r, ok := m.rates[src]
		if !ok {
			r = &srcRate{lastDone: done}
			m.rates[src] = r
		}
		inst := float64(done-r.lastDone) / dt
		if inst < 0 {
			inst = 0
		}
		r.ewma = alpha*inst + (1-alpha)*r.ewma
		r.lastDone = done
	}
}

func (m Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.searchActive {
		switch msg.String() {
		case "enter":
			m.searchActive = false
			m.search.Blur()
			return m, m.refreshTracksCmd(m.search.Value())
		case "esc":
			m.searchActive = false
			m.search.Blur()
			m.search.SetValue("")
			return m, m.refreshTracksCmd("")
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

	case "1":
		m.tab = tabDashboard
		return m, nil
	case "2":
		m.tab = tabTracks
		return m, nil
	case "3":
		m.tab = tabPlayer
		return m, nil
	case "4":
		m.tab = tabLog
		return m, nil
	case "5":
		if m.tab != tabBrowse {
			m.switchToBrowseTab()
		}
		return m, nil
	case "tab":
		m.tab = (m.tab + 1) % tabCount
		return m, nil

	case "/":
		m.searchActive = true
		m.search.Focus()
		return m, nil

	case "s":
		if m.eng.Paused() {
			m.eng.SetPaused(false)
			m.statusLine = "running"
		} else {
			m.eng.SetPaused(true)
			m.statusLine = "paused"
		}
		return m, nil

	case "r":
		if !m.discovering {
			m.discovering = true
			m.statusLine = "re-running discovery..."
			return m, m.discoverCmd()
		}
		return m, nil

	case "R":
		n, _ := m.eng.RetryFailed(m.ctx)
		m.statusLine = "retry: reset " + strconv.Itoa(int(n)) + " failed rows"
		return m, m.reconcileCmd()

	case "V":
		n, _ := m.eng.ReviveSkipped(m.ctx)
		m.statusLine = "revive: reset " + strconv.Itoa(int(n)) + " skipped rows"
		return m, m.reconcileCmd()

	case "d":
		m.focusPool = indexOf(poolStages, pipeline.StageDownload)
	case "c":
		m.focusPool = indexOf(poolStages, pipeline.StageConvert)
	case "k":
		m.focusPool = indexOf(poolStages, pipeline.StagePackage)
	case "x":
		m.focusPool = indexOf(poolStages, pipeline.StageCleaner)

	case "+", "=":
		m.eng.Scale(poolStages[m.focusPool], +1)
	case "-", "_":
		m.eng.Scale(poolStages[m.focusPool], -1)

	case "up", "ctrl+p":
		if m.tab == tabBrowse {
			if m.browseSel > 0 {
				m.browseSel--
			}
			return m, nil
		}
		if m.tab == tabTracks || m.tab == tabDashboard {
			if m.sel > 0 {
				m.sel--
				m.rememberSelection()
			}
		}
	case "down", "ctrl+n":
		if m.tab == tabBrowse {
			if m.browseSel < m.browseCount()-1 {
				m.browseSel++
			}
			return m, nil
		}
		if m.tab == tabTracks || m.tab == tabDashboard {
			if m.sel < len(m.tracks)-1 {
				m.sel++
				m.rememberSelection()
			}
		}
	case "g", "home":
		if m.tab == tabBrowse {
			m.browseSel = 0
			return m, nil
		}
		m.sel = 0
		m.rememberSelection()
	case "G", "end":
		if m.tab == tabBrowse {
			if count := m.browseCount(); count > 0 {
				m.browseSel = count - 1
			}
			return m, nil
		}
		if len(m.tracks) > 0 {
			m.sel = len(m.tracks) - 1
			m.rememberSelection()
		}

	case "enter":
		if m.tab == tabBrowse {
			return m.browseDrill()
		}
		return m, m.playSelected()
	case " ", "space":
		if m.play.Playing() || m.play.Current() != "" {
			if m.play.Playing() {
				m.play.Pause()
			} else {
				m.play.Resume()
			}
		} else {
			return m, m.playSelected()
		}
	case "right":
		m.play.Seek(10)
	case "left":
		m.play.Seek(-10)

	case "esc", "backspace":
		if m.tab == tabBrowse && m.browseLevel > 0 {
			return m.browseBack()
		}
	}
	return m, nil
}

func (m *Model) playSelected() tea.Cmd {
	if m == nil {
		return nil
	}
	if m.sel < 0 || m.sel >= len(m.tracks) {
		return nil
	}
	return m.playTrack(m.tracks[m.sel])
}

func (m *Model) playTrack(t *core.Track) tea.Cmd {
	if t == nil {
		return nil
	}
	if t.Status != core.StatusDone || t.CafPath == "" {
		m.statusLine = "not playable yet (status " + t.Status + ")"
		return nil
	}
	if m.play == nil || !m.play.Available() {
		m.statusLine = "no audio backend (install afplay/ffplay)"
		return nil
	}
	path := m.play.PreferredPath(m.cfg.Dir, t.OpusPath, t.CafPath)
	m.play.Play(path)
	m.nowPlaying = t.ID
	m.nowTitle = core.DisplayTitle(t, core.DisplayGlobal)
	m.statusLine = "playing: " + m.nowTitle
	return nil
}

func (m *Model) rememberSelection() {
	if m.sel >= 0 && m.sel < len(m.tracks) {
		m.selID = m.tracks[m.sel].ID
	}
}

func (m *Model) restoreSelection() {
	if m.selID != "" {
		for i, t := range m.tracks {
			if t.ID == m.selID {
				m.sel = i
				return
			}
		}
	}
	m.sel = clamp(m.sel, 0, max(0, len(m.tracks)-1))
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return 0
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m *Model) browseNodes() []db.BrowseEntry {
	switch m.browseLevel {
	case 0:
		return m.browseSources
	case 1:
		return m.browseComposers
	case 2:
		return m.browseTitles
	case 3:
		entries := make([]db.BrowseEntry, len(m.browseTracks))
		for i, t := range m.browseTracks {
			entries[i] = db.BrowseEntry{Name: core.DisplayTitle(t, core.DisplayWork), Key: t.ID}
		}
		return entries
	}
	return nil
}

func (m *Model) browseCount() int {
	switch m.browseLevel {
	case 0:
		return len(m.browseSources)
	case 1:
		return len(m.browseComposers)
	case 2:
		return len(m.browseTitles)
	case 3:
		return len(m.browseTracks)
	}
	return 0
}

func (m *Model) switchToBrowseTab() {
	m.tab = tabBrowse
	m.browseLevel = 0
	m.browseSel = 0
	m.browseSelSource = ""
	m.browseSelComposer = ""
	m.browseSelTitle = ""
	m.browseSelWorkKey = ""
	m.browseSources = nil
	m.browseComposers = nil
	m.browseTitles = nil
	m.browseTracks = nil
}

func (m Model) browseDrill() (tea.Model, tea.Cmd) {
	nodes := m.browseNodes()
	if m.browseSel < 0 || m.browseSel >= len(nodes) {
		return m, nil
	}
	sel := nodes[m.browseSel]
	switch m.browseLevel {
	case 0:
		m.browseLevel = 1
		m.browseSelSource = sel.Name
		m.browseSel = 0
		m.browseComposers = nil
		return m, m.refreshBrowseComposersCmd(sel.Key)
	case 1:
		m.browseLevel = 2
		m.browseSelComposer = sel.Key
		m.browseSel = 0
		m.browseTitles = nil
		return m, m.refreshBrowseTitlesCmd(m.browseSelSource, sel.Key)
	case 2:
		m.browseLevel = 3
		m.browseSelTitle = sel.Name
		m.browseSelWorkKey = sel.Key
		m.browseSel = 0
		m.browseTracks = nil
		return m, m.refreshBrowseTracksCmd(sel.Key)
	case 3:
		if m.browseSel >= 0 && m.browseSel < len(m.browseTracks) {
			return m, m.playTrack(m.browseTracks[m.browseSel])
		}
		return m, nil
	}
	return m, nil
}

func (m Model) browseBack() (tea.Model, tea.Cmd) {
	switch m.browseLevel {
	case 1:
		m.browseLevel = 0
		m.browseSelSource = ""
		m.browseSel = 0
		m.browseComposers = nil
		m.browseTracks = nil
		return m, m.refreshBrowseSourcesCmd()
	case 2:
		m.browseLevel = 1
		m.browseSelComposer = ""
		m.browseSel = 0
		m.browseTitles = nil
		m.browseTracks = nil
		return m, m.refreshBrowseComposersCmd(m.browseSelSource)
	case 3:
		m.browseLevel = 2
		m.browseSelTitle = ""
		m.browseSelWorkKey = ""
		m.browseSel = 0
		m.browseTracks = nil
		return m, m.refreshBrowseTitlesCmd(m.browseSelSource, m.browseSelComposer)
	}
	return m, nil
}

func (m Model) refreshBrowseCurrentCmd() tea.Cmd {
	switch m.browseLevel {
	case 0:
		return m.refreshBrowseSourcesCmd()
	case 1:
		return m.refreshBrowseComposersCmd(m.browseSelSource)
	case 2:
		return m.refreshBrowseTitlesCmd(m.browseSelSource, m.browseSelComposer)
	case 3:
		if m.browseSelWorkKey != "" {
			return m.refreshBrowseTracksCmd(m.browseSelWorkKey)
		}
		return nil
	}
	return nil
}
