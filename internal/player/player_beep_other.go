//go:build !darwin

package player

import (
	"context"
	"os/exec"
)

type beepHandle struct{}

// New detects audio backends and returns a Player.
func New() *Player {
	p := &Player{events: make(chan Event, 32)}
	if bin, err := exec.LookPath("ffplay"); err == nil {
		p.backend, p.bin = "ffplay", bin
	}
	return p
}

func (p *Player) Play(path string) {
	if !p.Available() {
		p.send(Event{Path: path, Err: "no audio backend"})
		return
	}
	p.mu.Lock()
	gen := 1
	if p.playing != nil {
		p.playing.cancel()
		gen = p.playing.gen + 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	pb := &playback{ctx: ctx, cancel: cancel, path: path, gen: gen, dur: probeDuration(path)}
	p.playing = pb
	p.mu.Unlock()

	go p.playSubprocess(pb)
}

func (p *Player) Stop() {
	p.mu.Lock()
	if p.playing != nil {
		p.playing.cancel()
	}
	p.playing = nil
	p.mu.Unlock()
	p.send(Event{State: "stopped"})
}

func (p *Player) Pause() {}

func (p *Player) Resume() {}

func (p *Player) Seek(relSec float64) {}

func (p *Player) Position() (pos, dur float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.playing != nil {
		return 0, p.playing.dur
	}
	return 0, 0
}

func (p *Player) Playing() bool { return false }
