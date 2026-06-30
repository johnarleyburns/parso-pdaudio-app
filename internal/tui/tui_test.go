package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/johnarleyburns/parso-pdaudio/internal/config"
	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"github.com/johnarleyburns/parso-pdaudio/internal/db"
	"github.com/johnarleyburns/parso-pdaudio/internal/pipeline"
	"github.com/johnarleyburns/parso-pdaudio/internal/player"
)

func newTestModel(t *testing.T) Model {
	t.Helper()
	cfg, err := config.Parse([]string{"--dir", t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	eng, err := pipeline.NewEngine(cfg, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := New(context.Background(), cfg, eng, store, player.New(), []string{"chopin", "marine"}, true)
	return m
}

func send(m Model, msg tea.Msg) Model {
	nm, _ := m.Update(msg)
	return nm.(Model)
}

func key(s string) tea.KeyMsg {
	switch s {
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestViewRendersAndNavigates(t *testing.T) {
	m := newTestModel(t)
	m = send(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = send(m, countsMsg{counts: map[string]map[string]int{
		"chopin": {"done": 2, "discovered": 1},
		"marine": {"downloaded": 3},
	}})
	tracks := []*core.Track{
		{ID: "01A", Source: "chopin", Status: core.StatusDiscovered, Title: "Ballade No. 1", Composer: "Chopin"},
		{ID: "01B", Source: "chopin", Status: core.StatusDone, Title: "Ballade No. 2", Composer: "Chopin",
			OpusPath: "01B.opus", CafPath: "01B.caf", OpusBytes: 1000, CafBytes: 1000},
	}
	m = send(m, tracksMsg{tracks: tracks})

	view := m.View()
	for _, want := range []string{"parso-pdaudio", "chopin", "marine", "Ballade", "TOTAL", "TRACKS"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}

	// Navigation.
	if m.sel != 0 {
		t.Fatalf("initial sel = %d", m.sel)
	}
	m = send(m, key("down"))
	if m.sel != 1 || m.selID != "01B" {
		t.Fatalf("after down: sel=%d selID=%s", m.sel, m.selID)
	}
	m = send(m, key("up"))
	if m.sel != 0 {
		t.Fatalf("after up: sel=%d", m.sel)
	}

	// Enter on a non-done track must not play.
	m = send(m, key("enter"))
	if m.nowPlaying != "" || !strings.Contains(m.statusLine, "not playable") {
		t.Fatalf("expected 'not playable', got nowPlaying=%q status=%q", m.nowPlaying, m.statusLine)
	}
}

func TestPauseAndSearchToggle(t *testing.T) {
	m := newTestModel(t)
	m = send(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	m = send(m, key("p"))
	if !m.eng.Paused() {
		t.Fatal("expected paused after 'p'")
	}
	m = send(m, key("s"))
	if m.eng.Paused() {
		t.Fatal("expected running after 's'")
	}

	// Search activate/cancel.
	m = send(m, key("/"))
	if !m.searchActive {
		t.Fatal("search should be active after '/'")
	}
	m = send(m, key("esc"))
	if m.searchActive {
		t.Fatal("search should be inactive after esc")
	}

	// Pool focus cycles with tab.
	before := m.focusPool
	m = send(m, key("tab"))
	if m.focusPool == before {
		t.Fatal("tab should change focused pool")
	}
}
