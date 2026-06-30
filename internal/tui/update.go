package tui

import (
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"github.com/johnarleyburns/parso-pdaudio/internal/pipeline"
)

// Update is the Bubble Tea event loop.
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
		if msg.Ended && m.nowPlaying != "" {
			m.nowPlaying = ""
			if msg.Err != "" {
				m.statusLine = "playback error: " + msg.Err
			} else {
				m.statusLine = "playback finished"
			}
		}
		return m, listenPlayer(m.play.Events())

	case countsMsg:
		m.counts = msg.counts
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

	cmds := []tea.Cmd{tickCmd(), m.reconcileCmd()}
	if m.tickCount%2 == 0 && !m.searchActive {
		cmds = append(cmds, m.refreshTracksCmd(m.search.Value()))
	}
	return m, tea.Batch(cmds...)
}

// updateRates maintains a per-source EWMA of track-completion rate for ETA.
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

	case "/":
		m.searchActive = true
		m.search.Focus()
		return m, nil

	case "s":
		m.eng.SetPaused(false)
		m.statusLine = "running"
		return m, nil
	case "p":
		m.eng.SetPaused(true)
		m.statusLine = "paused"
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

	case "tab":
		m.focusPool = (m.focusPool + 1) % len(poolStages)

	case "up", "ctrl+p":
		if m.sel > 0 {
			m.sel--
			m.rememberSelection()
		}
	case "down", "ctrl+n":
		if m.sel < len(m.tracks)-1 {
			m.sel++
			m.rememberSelection()
		}
	case "g", "home":
		m.sel = 0
		m.rememberSelection()
	case "G", "end":
		if len(m.tracks) > 0 {
			m.sel = len(m.tracks) - 1
			m.rememberSelection()
		}

	case "enter":
		return m, m.playSelected()
	case " ", "space":
		m.play.Stop()
		m.nowPlaying = ""
		m.statusLine = "stopped"
	}
	return m, nil
}

func (m *Model) playSelected() tea.Cmd {
	if m.sel < 0 || m.sel >= len(m.tracks) {
		return nil
	}
	t := m.tracks[m.sel]
	if t.Status != core.StatusDone || t.OpusPath == "" {
		m.statusLine = "not playable yet (status " + t.Status + ")"
		return nil
	}
	if !m.play.Available() {
		m.statusLine = "no audio backend (install afplay/ffplay)"
		return nil
	}
	path := m.play.PreferredPath(m.cfg.Dir, t.OpusPath, t.CafPath)
	m.play.Play(path)
	m.nowPlaying = t.ID
	m.statusLine = "playing: " + displayTitle(t)
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
