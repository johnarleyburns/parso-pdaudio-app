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

	// Switch to Tracks tab (2).
	m = send(m, key("2"))
	if m.tab != tabTracks {
		t.Fatalf("expected tab %d, got %d", tabTracks, m.tab)
	}

	view := m.View()
	for _, want := range []string{"parso-pdaudio", "Ballade", "TITLE", "Tracks", "COMPOSER", "Chopin"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	// Dashboard tab should show sources.
	m2 := m
	m2 = send(m2, key("1"))
	dashView := m2.View()
	for _, want := range []string{"chopin", "marine"} {
		if !strings.Contains(dashView, want) {
			t.Fatalf("dashboard missing %q:\n%s", want, dashView)
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

func TestTrackTableLayout(t *testing.T) {
	m := newTestModel(t)
	m = send(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	tracks := []*core.Track{
		{ID: "01A", Source: "chopin", Status: core.StatusDone, Title: "Ballade No. 1", Composer: "Chopin",
			OpusPath: "01A.opus", CafPath: "01A.caf", OpusBytes: 5000, CafBytes: 8000},
		{ID: "02A", Source: "musopen", Status: core.StatusDiscovered, Title: "Symphony No. 5", Composer: ""},
		{ID: "03A", Source: "airforce", Status: core.StatusFailed, Title: "Hymn", Composer: "John Doe"},
	}
	m = send(m, tracksMsg{tracks: tracks})
	m = send(m, key("2"))

	view := m.View()
	lines := strings.Split(view, "\n")

	// Find box boundaries and their line numbers.
	boxTop := -1
	boxBot := -1
	for i, l := range lines {
		if boxTop == -1 && strings.Contains(l, "╭") {
			boxTop = i
		}
		if strings.Contains(l, "╰") {
			boxBot = i
		}
	}
	if boxTop < 0 || boxBot < 0 || boxBot <= boxTop {
		t.Fatalf("box not found: top=%d bot=%d\nview:\n%s", boxTop, boxBot, view)
	}

	// Header must be inside the box (after boxTop, before boxBot).
	// The header line has the column titles.
	headerLine := ""
	foundCOMPOSER := false
	foundST := false
	for i := boxTop + 1; i < boxBot; i++ {
		line := lines[i]
		if strings.Contains(line, "COMPOSER") {
			foundCOMPOSER = true
			headerLine = line
		}
		if strings.Contains(line, "ST") {
			foundST = true
		}
	}
	if !foundCOMPOSER {
		t.Fatalf("COMPOSER column header not inside box\nview:\n%s", view)
	}
	if !foundST {
		t.Fatalf("ST column header not inside box\nview:\n%s", view)
	}

	// Header must NOT appear before the box top.
	for i := 0; i < boxTop; i++ {
		if strings.Contains(lines[i], "COMPOSER") || strings.Contains(lines[i], "SOURCE") {
			t.Fatalf("column header appears before box at line %d\nview:\n%s", i, view)
		}
	}

	// Data rows: inside the box, verify composer values.
	foundChopin := false
	foundDash := false
	for i := boxTop + 1; i < boxBot; i++ {
		line := lines[i]
		// Chopin has composer "Chopin"
		if strings.Contains(line, "●done") {
			if !strings.Contains(line, "Chopin") {
				t.Errorf("expected composer 'Chopin' in done row:\n%s", line)
			}
			foundChopin = true
		}
		// Track without composer should show "—"
		if strings.Contains(line, "Symphony") {
			// The composer column should contain "—" somewhere near it
			if !strings.Contains(line, "—") {
				t.Errorf("expected '—' for missing composer in discovered row:\n%s", line)
			}
			foundDash = true
		}
	}
	if !foundChopin {
		t.Errorf("done track row with composer not found\nview:\n%s", view)
	}
	if !foundDash {
		t.Errorf("track without composer not found\nview:\n%s", view)
	}

	// Info line should be inside the box.
	foundInfo := false
	for i := boxTop + 1; i < boxBot; i++ {
		if strings.Contains(lines[i], "3 shown") {
			foundInfo = true
		}
	}
	if !foundInfo {
		t.Errorf("'3 shown' info line not inside box\nview:\n%s", view)
	}

	// Dump header line for debugging.
	t.Logf("header line: %q", headerLine)
}

func TestPlaySelectedNoPanic(t *testing.T) {
	m := newTestModel(t)
	_ = send(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	// nil player
	m.play = nil
	if err := recoverPanic(func() { m.playSelected() }); err != nil {
		t.Fatalf("playSelected panicked with nil player: %v", err)
	}
	m = newTestModel(t) // reset

	// Empty tracks
	m.tracks = nil
	if err := recoverPanic(func() { m.playSelected() }); err != nil {
		t.Fatalf("playSelected panicked with nil tracks: %v", err)
	}

	// Negative selection
	m.tracks = []*core.Track{{ID: "X", Status: core.StatusDone, OpusPath: "/x.opus"}}
	m.sel = -1
	if err := recoverPanic(func() { m.playSelected() }); err != nil {
		t.Fatalf("playSelected panicked with sel=-1: %v", err)
	}

	// OOB selection
	m.sel = 5
	if err := recoverPanic(func() { m.playSelected() }); err != nil {
		t.Fatalf("playSelected panicked with OOB sel: %v", err)
	}

	// Not done track
	m.sel = 0
	m.tracks[0].Status = core.StatusDownloading
	if err := recoverPanic(func() { m.playSelected() }); err != nil {
		t.Fatalf("playSelected panicked on non-done track: %v", err)
	}
	if !strings.Contains(m.statusLine, "not playable") {
		t.Errorf("expected 'not playable', got %q", m.statusLine)
	}

	// Done track with backend — tests the player nil deref path.
	// Skip beep backend under -race (oto/v3 internal race on darwin).
	m = newTestModel(t)
	if m.play != nil && m.play.Backend() == "beep" && raceDetector {
		t.Skip("skipping beep under -race (oto/v3 internal race on darwin)")
	}
	doneT := &core.Track{ID: "OK", Source: "test", Status: core.StatusDone,
		Title: "Safe", OpusPath: "safe.opus", CafPath: "safe.caf"}
	m.tracks = []*core.Track{doneT}
	m.sel = 0
	if err := recoverPanic(func() { m.playSelected() }); err != nil {
		t.Fatalf("playSelected panicked on done track: %v", err)
	}
}

func recoverPanic(f func()) (recovered any) {
	defer func() { recovered = recover() }()
	f()
	return nil
}

func TestPauseAndSearchToggle(t *testing.T) {
	m := newTestModel(t)
	m = send(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	// 's' toggles pause (first press pauses).
	m = send(m, key("s"))
	if !m.eng.Paused() {
		t.Fatal("expected paused after 's'")
	}
	m = send(m, key("s"))
	if m.eng.Paused() {
		t.Fatal("expected running after second 's'")
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

	// Tab switches tabs (1-4), not pool focus. Pool cycling removed from tab.
	before := m.tab
	m = send(m, key("tab"))
	if m.tab == before {
		t.Fatal("tab should change active tab")
	}
}
