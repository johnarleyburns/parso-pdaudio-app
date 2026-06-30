// Package player provides an in-process audio playback engine using
// gopxl/beep + ebittengine/oto (CoreAudio on macOS) or afplay/ffplay subprocess.
package player

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// Event is emitted when track state changes.
type Event struct {
	Path   string
	Ended  bool
	PosSec float64
	DurSec float64
	Err    string
	State  string // loading | playing | paused | stopped
}

// Player coordinates playback.
type Player struct {
	mu      sync.Mutex
	playing *playback

	events  chan Event
	backend string // "beep" | "afplay" | "ffplay" | ""
	bin     string // afplay or ffplay binary

	bph beepHandle // platform-specific beep state
}

type playback struct {
	ctx    context.Context
	cancel context.CancelFunc
	path   string
	dur    float64
	gen    int
	cmd    *exec.Cmd // for fallback subprocess
}

func (p *Player) Available() bool      { return p.backend != "" }
func (p *Player) Backend() string      { return p.backend }
func (p *Player) Events() <-chan Event { return p.events }

func (p *Player) PreferredPath(dir, opusRel, cafRel string) string {
	opus := filepath.Join(dir, opusRel)
	caf := filepath.Join(dir, cafRel)
	if p.backend == "beep" || p.backend == "afplay" {
		if cafRel != "" {
			if _, err := os.Stat(caf); err == nil {
				return caf
			}
		}
		if opusRel != "" {
			if _, err := os.Stat(opus); err == nil {
				return opus
			}
		}
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

func (p *Player) Current() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.playing != nil {
		return p.playing.path
	}
	return ""
}

func (p *Player) send(ev Event) {
	select {
	case p.events <- ev:
	default:
	}
}

func probeDuration(path string) float64 {
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	var d float64
	fmt.Sscanf(string(out), "%f", &d)
	return d
}

func (p *Player) playSubprocessFallback(pb *playback) {
	if runtime.GOOS == "darwin" {
		p.backend = "afplay"
		if bin, err := exec.LookPath("afplay"); err == nil {
			p.bin = bin
		}
	}
	if p.backend == "" || p.backend == "beep" {
		if bin, err := exec.LookPath("ffplay"); err == nil {
			p.backend, p.bin = "ffplay", bin
		}
	}
	p.playSubprocess(pb)
}

func (p *Player) playSubprocess(pb *playback) {
	var cmd *exec.Cmd
	if p.backend == "ffplay" {
		cmd = exec.CommandContext(pb.ctx, p.bin, "-nodisp", "-autoexit", "-loglevel", "quiet", pb.path)
	} else {
		cmd = exec.CommandContext(pb.ctx, p.bin, pb.path)
	}
	pb.cmd = cmd

	p.send(Event{State: "playing", Path: pb.path, DurSec: pb.dur})

	err := cmd.Run()
	p.mu.Lock()
	stillCurrent := p.playing != nil && p.playing.gen == pb.gen
	if stillCurrent {
		p.playing = nil
	}
	p.mu.Unlock()
	if !stillCurrent {
		return
	}
	ev := Event{Path: pb.path, Ended: true, State: "stopped"}
	if err != nil && pb.ctx.Err() == nil {
		ev.Err = err.Error()
	}
	p.send(ev)
}
