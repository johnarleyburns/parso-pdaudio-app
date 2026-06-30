// Package player provides simple file playback for the TUI's built-in player.
// On macOS it uses afplay (CoreAudio — the same stack iOS uses, so it doubles
// as a CAF correctness check); elsewhere it falls back to ffplay.
package player

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// Event is emitted when a track finishes or fails playback.
type Event struct {
	Path  string
	Ended bool
	Err   string
}

// Player plays one file at a time via an external CoreAudio/ffmpeg backend.
type Player struct {
	backend string // "afplay" | "ffplay" | ""
	bin     string

	mu      sync.Mutex
	cancel  context.CancelFunc
	current string
	gen     int

	events chan Event
}

// New detects a playback backend and returns a Player.
func New() *Player {
	p := &Player{events: make(chan Event, 16)}
	if runtime.GOOS == "darwin" {
		if bin, err := exec.LookPath("afplay"); err == nil {
			p.backend, p.bin = "afplay", bin
			return p
		}
	}
	if bin, err := exec.LookPath("ffplay"); err == nil {
		p.backend, p.bin = "ffplay", bin
	}
	return p
}

// Available reports whether a backend was found.
func (p *Player) Available() bool { return p.backend != "" }

// Backend returns the backend name ("afplay", "ffplay", or "").
func (p *Player) Backend() string { return p.backend }

// Events returns the channel of playback lifecycle events.
func (p *Player) Events() <-chan Event { return p.events }

// PreferredPath picks the artifact to play for a track given its directory.
// afplay validates the CAF (Apple stack); ffplay prefers the opus.
func (p *Player) PreferredPath(dir, opusRel, cafRel string) string {
	opus := filepath.Join(dir, opusRel)
	caf := filepath.Join(dir, cafRel)
	if p.backend == "afplay" {
		if cafRel != "" {
			return caf
		}
		return opus
	}
	if opusRel != "" {
		return opus
	}
	return caf
}

// Play stops any current playback and starts the given file asynchronously.
func (p *Player) Play(path string) {
	if !p.Available() {
		p.send(Event{Path: path, Err: "no audio backend (afplay/ffplay) found"})
		return
	}
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
	}
	p.gen++
	gen := p.gen
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.current = path
	p.mu.Unlock()

	var cmd *exec.Cmd
	if p.backend == "ffplay" {
		cmd = exec.CommandContext(ctx, p.bin, "-nodisp", "-autoexit", "-loglevel", "quiet", path)
	} else {
		cmd = exec.CommandContext(ctx, p.bin, path)
	}

	go func() {
		err := cmd.Run()
		p.mu.Lock()
		stillCurrent := p.gen == gen
		if stillCurrent {
			p.current = ""
			p.cancel = nil
		}
		p.mu.Unlock()
		if !stillCurrent {
			return // superseded by a newer Play/Stop
		}
		ev := Event{Path: path, Ended: true}
		if err != nil && ctx.Err() == nil {
			ev.Err = err.Error()
		}
		p.send(ev)
	}()
}

// Stop halts current playback.
func (p *Player) Stop() {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.current = ""
	p.gen++
	p.mu.Unlock()
}

// Playing reports whether a track is currently playing.
func (p *Player) Playing() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current != ""
}

// Current returns the path currently playing (empty if stopped).
func (p *Player) Current() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current
}

func (p *Player) send(ev Event) {
	select {
	case p.events <- ev:
	default:
	}
}
