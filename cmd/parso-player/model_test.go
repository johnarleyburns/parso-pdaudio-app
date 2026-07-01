package main

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"github.com/johnarleyburns/parso-pdaudio/internal/db"
	"github.com/johnarleyburns/parso-pdaudio/internal/player"
)

// seedDB builds a temp library with two composers, one multi-movement work, and
// FTS rows, so the model's browse/search paths can be exercised offline.
func seedDB(t *testing.T) *db.DB {
	t.Helper()
	store, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	ins := func(id, source, composer, workID, workTitle, title, display string, mvt int, kws string) {
		_, err := store.SQL().ExecContext(ctx, `
INSERT INTO tracks (id, source, title, composer, status, caf_path, original_url,
  work_id, work_title, movement_index, display_title, duration_sec, created_at, updated_at)
VALUES (?,?,?,?,'done',?,?,?,?,?,?,120, strftime('%s','now'), strftime('%s','now'))`,
			id, source, title, composer, id+".caf", "http://example/"+id,
			workID, workTitle, mvt, display)
		if err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
		if err := store.Index(ctx, id, splitWords(kws), kws); err != nil {
			t.Fatalf("index %s: %v", id, err)
		}
	}
	if err := store.UpsertWork(ctx, &core.Work{ID: "w1", ComposerCanonical: "Ludwig van Beethoven", Title: "Symphony No. 5", TrackCount: 2}); err != nil {
		t.Fatal(err)
	}
	ins("01A", "orchestral", "Ludwig van Beethoven", "w1", "Symphony No. 5", "mvt1",
		"Ludwig van Beethoven — Symphony No. 5 · I. Allegro", 1, "beethoven symphony allegro")
	ins("01B", "orchestral", "Ludwig van Beethoven", "w1", "Symphony No. 5", "mvt2",
		"Ludwig van Beethoven — Symphony No. 5 · II. Andante", 2, "beethoven symphony andante")
	ins("01C", "piano", "Frédéric Chopin", "w2", "Ballade No. 1", "ballade",
		"Frédéric Chopin — Ballade No. 1", 0, "chopin ballade")
	return store
}

func splitWords(s string) []string { return strings.Fields(s) }

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func exec(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

func step(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	nm, _ := m.Update(msg)
	return nm.(model)
}

// send applies a key, then executes the returned command and applies its message.
func send(t *testing.T, m model, k string) model {
	t.Helper()
	nm, cmd := m.Update(key(k))
	m = nm.(model)
	if msg := exec(cmd); msg != nil {
		m = step(t, m, msg)
	}
	return m
}

func TestModelBrowseDrilldown(t *testing.T) {
	store := seedDB(t)
	defer store.Close()
	m := newModel(store, player.New(), nil, t.TempDir())

	// Init loads sources.
	m = step(t, m, exec(m.Init()))
	if m.level != 0 || len(m.nodes) != 2 {
		t.Fatalf("level0: want 2 sources, got level=%d n=%d", m.level, len(m.nodes))
	}

	// Drill: source -> composer.
	m = send(t, m, "enter")
	if m.level != 1 || len(m.nodes) == 0 {
		t.Fatalf("level1 composers: level=%d n=%d", m.level, len(m.nodes))
	}
	// Drill: composer -> works.
	m = send(t, m, "enter")
	if m.level != 2 || len(m.nodes) == 0 {
		t.Fatalf("level2 works: level=%d n=%d", m.level, len(m.nodes))
	}
	// Drill: work -> tracks (movements).
	m = send(t, m, "enter")
	if m.level != 3 || len(m.tracks) == 0 {
		t.Fatalf("level3 tracks: level=%d n=%d", m.level, len(m.tracks))
	}
	// Movements should be ordered by movement index.
	if len(m.tracks) >= 2 && m.tracks[0].MovementIndex > m.tracks[1].MovementIndex {
		t.Fatalf("tracks not ordered by movement index: %+v", m.tracks)
	}

	// Back up one level returns to works.
	m = send(t, m, "backspace")
	if m.level != 2 {
		t.Fatalf("after back: want level 2, got %d", m.level)
	}
}

func TestModelSearch(t *testing.T) {
	store := seedDB(t)
	defer store.Close()
	m := newModel(store, player.New(), nil, t.TempDir())
	m = step(t, m, exec(m.Init()))

	// Enter search mode, type a query, run it.
	m = send(t, m, "/")
	if m.mode != modeSearch {
		t.Fatalf("expected search mode")
	}
	m.search.SetValue("beethoven")
	nm, cmd := m.Update(key("enter"))
	m = nm.(model)
	m = step(t, m, exec(cmd))
	if len(m.tracks) < 2 {
		t.Fatalf("search 'beethoven' expected >=2 tracks, got %d", len(m.tracks))
	}
	for _, tr := range m.tracks {
		if tr.Composer != "Ludwig van Beethoven" {
			t.Fatalf("unexpected composer in results: %q", tr.Composer)
		}
	}
}

func TestModelPlayWithoutClientReportsError(t *testing.T) {
	store := seedDB(t)
	defer store.Close()
	m := newModel(store, player.New(), nil, t.TempDir()) // nil R2 client

	// Search so we have tracks, then attempt to play the first result.
	m = step(t, m, exec(m.Init()))
	m = send(t, m, "/")
	m.search.SetValue("chopin")
	nm, cmd := m.Update(key("enter"))
	m = nm.(model)
	m = step(t, m, exec(cmd))
	if len(m.tracks) == 0 {
		t.Fatal("no chopin results")
	}
	// Enter on a search result triggers playTrack; with no R2 client it should
	// yield a playedMsg error rather than panicking.
	_, cmd = m.Update(key("enter"))
	msg := exec(cmd)
	pm, ok := msg.(playedMsg)
	if !ok {
		t.Fatalf("expected playedMsg, got %T", msg)
	}
	if pm.err == "" {
		t.Fatal("expected an error playing without an R2 client")
	}
}
