package player

import (
	"testing"
	"time"
)

func TestPreferredPath(t *testing.T) {
	p := &Player{backend: "afplay"}
	if got := p.PreferredPath("/lib", "a.opus", "a.caf"); got != "/lib/a.caf" {
		t.Fatalf("afplay should prefer caf, got %q", got)
	}
	if got := p.PreferredPath("/lib", "a.opus", ""); got != "/lib/a.opus" {
		t.Fatalf("afplay should fall back to opus, got %q", got)
	}
	pf := &Player{backend: "ffplay"}
	if got := pf.PreferredPath("/lib", "a.opus", "a.caf"); got != "/lib/a.opus" {
		t.Fatalf("ffplay should prefer opus, got %q", got)
	}
}

func TestNoBackendEmitsError(t *testing.T) {
	p := &Player{events: make(chan Event, 4)} // no backend
	if p.Available() {
		t.Fatal("expected no backend")
	}
	p.Play("/nope.caf")
	select {
	case ev := <-p.Events():
		if ev.Err == "" {
			t.Fatal("expected error event for no backend")
		}
	case <-time.After(time.Second):
		t.Fatal("no event emitted")
	}
}

func TestPlayingStateDefaults(t *testing.T) {
	p := New()
	if p.Playing() {
		t.Fatal("new player should not be playing")
	}
	if p.Current() != "" {
		t.Fatal("new player current should be empty")
	}
}

func TestPlayNoPanicOnFirstCall(t *testing.T) {
	p := New()
	if !p.Available() {
		t.Skip("no audio backend available")
	}
	// Must not panic when p.playing is nil (first Play call).
	p.Play("/dev/null")
	// Clean up.
	p.Stop()
}
