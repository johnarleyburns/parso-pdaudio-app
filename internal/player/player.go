// Package player provides an in-process audio playback engine using
// gopxl/beep + ebittengine/oto for PCM output via CoreAudio (macOS) or
// ALSA (Linux). Falls back to afplay/ffplay subprocess.
package player

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/speaker"
	"github.com/gopxl/beep/v2/wav"
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

	// Beep pipeline
	streamer    beep.StreamSeekCloser
	ctrl        *beep.Ctrl
	format      beep.Format
	sampleRate  beep.SampleRate
	speakerInit bool
}

type playback struct {
	ctx    context.Context
	cancel context.CancelFunc
	path   string
	dur    float64
	gen    int
	cmd    *exec.Cmd // for fallback subprocess
}

// New detects audio backends and returns a Player.
func New() *Player {
	p := &Player{events: make(chan Event, 32)}

	if runtime.GOOS == "darwin" {
		// On macOS, try beep/oto (CoreAudio via ffmpeg decode).
		if _, err := exec.LookPath("ffmpeg"); err == nil {
			p.backend = "beep"
			return p
		}
		// Fallback: built-in afplay.
		if bin, err := exec.LookPath("afplay"); err == nil {
			p.backend, p.bin = "afplay", bin
			return p
		}
	}

	// Linux / other: try ffplay (shipped with ffmpeg).
	if p.backend == "" {
		if bin, err := exec.LookPath("ffplay"); err == nil {
			p.backend, p.bin = "ffplay", bin
		}
	}
	return p
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

	if p.backend == "beep" {
		go p.playBeep(pb)
	} else {
		go p.playSubprocess(pb)
	}
}

func (p *Player) Stop() {
	p.mu.Lock()
	if p.playing != nil {
		p.playing.cancel()
	}
	if p.streamer != nil {
		speaker.Clear()
		p.streamer.Close()
		p.streamer = nil
		p.ctrl = nil
	}
	p.playing = nil
	p.mu.Unlock()
	p.send(Event{State: "stopped"})
}

func (p *Player) Pause() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctrl == nil {
		return
	}
	speaker.Lock()
	p.ctrl.Paused = true
	speaker.Unlock()
	p.send(Event{State: "paused"})
}

func (p *Player) Resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctrl == nil {
		return
	}
	speaker.Lock()
	p.ctrl.Paused = false
	speaker.Unlock()
	p.send(Event{State: "playing"})
}

func (p *Player) Seek(relSec float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.streamer == nil || p.format.SampleRate == 0 {
		return
	}
	speaker.Lock()
	pos := p.streamer.Position()
	length := p.streamer.Len()
	delta := p.format.SampleRate.N(time.Duration(relSec * float64(time.Second)))
	newPos := pos + delta
	if newPos < 0 {
		newPos = 0
	}
	if length > 0 && newPos >= length {
		newPos = length - 1
	}
	if newPos >= 0 {
		_ = p.streamer.Seek(newPos)
	}
	speaker.Unlock()
}

func (p *Player) Position() (pos, dur float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.streamer == nil || p.format.SampleRate == 0 {
		if p.playing != nil {
			return 0, p.playing.dur
		}
		return 0, 0
	}
	speaker.Lock()
	samplePos := p.streamer.Position()
	sampleLen := p.streamer.Len()
	speaker.Unlock()
	pos = p.format.SampleRate.D(samplePos).Seconds()
	dur = p.format.SampleRate.D(sampleLen).Seconds()
	if dur == 0 && p.playing != nil {
		dur = p.playing.dur
	}
	return
}

func (p *Player) Playing() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ctrl != nil && !p.ctrl.Paused
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

// playBeep decodes the audio file via ffmpeg and plays it through beep/oto.
func (p *Player) playBeep(pb *playback) {
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-i", pb.path,
		"-f", "wav", "-acodec", "pcm_s16le",
		"-ar", "44100", "-ac", "2",
		"pipe:1",
	}
	cmd := exec.CommandContext(pb.ctx, "ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		p.send(Event{Path: pb.path, Err: err.Error(), Ended: true})
		return
	}
	if err := cmd.Start(); err != nil {
		p.send(Event{Path: pb.path, Err: err.Error(), Ended: true})
		return
	}
	pb.cmd = cmd

	// Decode WAV stream with beep.
	streamer, format, err := wav.Decode(stdout)
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		p.playSubprocessFallback(pb)
		return
	}

	p.mu.Lock()
	p.streamer = streamer
	p.format = format
	p.ctrl = &beep.Ctrl{Streamer: streamer, Paused: false}
	p.mu.Unlock()

	// One-time speaker init.
	if !p.speakerInit {
		bufSize := format.SampleRate.N(time.Second / 10)
		if err := speaker.Init(format.SampleRate, bufSize); err != nil {
			p.send(Event{Path: pb.path, Err: fmt.Sprintf("audio init: %v", err), Ended: true})
			streamer.Close()
			cmd.Process.Kill()
			cmd.Wait()
			return
		}
		p.sampleRate = format.SampleRate
		p.speakerInit = true
	}

	// Resample if sample rate differs from the first initialised stream.
	p.mu.Lock()
	var s beep.Streamer = streamer
	if format.SampleRate != p.sampleRate {
		s = beep.Resample(4, format.SampleRate, p.sampleRate, streamer)
	}
	p.ctrl.Streamer = s
	p.mu.Unlock()

	p.send(Event{State: "loading", Path: pb.path, DurSec: pb.dur})

	done := make(chan struct{})
	speaker.Play(beep.Seq(p.ctrl, beep.Callback(func() {
		close(done)
	})))
	p.send(Event{State: "playing", Path: pb.path, DurSec: pb.dur})

	// Wait until track finishes or is cancelled.
	select {
	case <-done:
	case <-pb.ctx.Done():
	}

	cmd.Wait()
	p.mu.Lock()
	if p.streamer == streamer {
		p.streamer.Close()
		p.streamer = nil
		p.ctrl = nil
	}
	stillCurrent := p.playing != nil && p.playing.gen == pb.gen
	if stillCurrent {
		p.playing = nil
	}
	p.mu.Unlock()
	if stillCurrent {
		p.send(Event{Path: pb.path, Ended: true, DurSec: pb.dur, State: "stopped"})
	}
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
