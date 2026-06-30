//go:build darwin

package player

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/speaker"
	"github.com/gopxl/beep/v2/wav"
)

type beepHandle struct {
	streamer    beep.StreamSeekCloser
	ctrl        *beep.Ctrl
	format      beep.Format
	sampleRate  beep.SampleRate
	speakerInit bool
}

// New detects audio backends and returns a Player.
func New() *Player {
	p := &Player{events: make(chan Event, 32)}
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		p.backend = "beep"
	}
	if p.backend == "" {
		if bin, err := exec.LookPath("afplay"); err == nil {
			p.backend, p.bin = "afplay", bin
		}
	}
	if p.backend == "" {
		if bin, err := exec.LookPath("ffplay"); err == nil {
			p.backend, p.bin = "ffplay", bin
		}
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
	if p.bph.streamer != nil {
		speaker.Clear()
		p.bph.streamer.Close()
		p.bph.streamer = nil
		p.bph.ctrl = nil
	}
	p.playing = nil
	p.mu.Unlock()
	p.send(Event{State: "stopped"})
}

func (p *Player) Pause() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bph.ctrl == nil {
		return
	}
	speaker.Lock()
	p.bph.ctrl.Paused = true
	speaker.Unlock()
	p.send(Event{State: "paused"})
}

func (p *Player) Resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bph.ctrl == nil {
		return
	}
	speaker.Lock()
	p.bph.ctrl.Paused = false
	speaker.Unlock()
	p.send(Event{State: "playing"})
}

func (p *Player) Seek(relSec float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bph.streamer == nil || p.bph.format.SampleRate == 0 {
		return
	}
	speaker.Lock()
	pos := p.bph.streamer.Position()
	length := p.bph.streamer.Len()
	delta := p.bph.format.SampleRate.N(time.Duration(relSec * float64(time.Second)))
	newPos := pos + delta
	if newPos < 0 {
		newPos = 0
	}
	if length > 0 && newPos >= length {
		newPos = length - 1
	}
	if newPos >= 0 {
		_ = p.bph.streamer.Seek(newPos)
	}
	speaker.Unlock()
}

func (p *Player) Position() (pos, dur float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bph.streamer == nil || p.bph.format.SampleRate == 0 {
		if p.playing != nil {
			return 0, p.playing.dur
		}
		return 0, 0
	}
	speaker.Lock()
	samplePos := p.bph.streamer.Position()
	sampleLen := p.bph.streamer.Len()
	speaker.Unlock()
	pos = p.bph.format.SampleRate.D(samplePos).Seconds()
	dur = p.bph.format.SampleRate.D(sampleLen).Seconds()
	if dur == 0 && p.playing != nil {
		dur = p.playing.dur
	}
	return
}

func (p *Player) Playing() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bph.ctrl != nil && !p.bph.ctrl.Paused
}

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

	streamer, format, err := wav.Decode(stdout)
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		p.playSubprocessFallback(pb)
		return
	}

	p.mu.Lock()
	p.bph.streamer = streamer
	p.bph.format = format
	p.bph.ctrl = &beep.Ctrl{Streamer: streamer, Paused: false}
	p.mu.Unlock()

	if !p.bph.speakerInit {
		bufSize := format.SampleRate.N(time.Second / 10)
		if err := speaker.Init(format.SampleRate, bufSize); err != nil {
			p.send(Event{Path: pb.path, Err: fmt.Sprintf("audio init: %v", err), Ended: true})
			streamer.Close()
			cmd.Process.Kill()
			cmd.Wait()
			return
		}
		p.bph.sampleRate = format.SampleRate
		p.bph.speakerInit = true
	}

	p.mu.Lock()
	var s beep.Streamer = streamer
	if format.SampleRate != p.bph.sampleRate {
		s = beep.Resample(4, format.SampleRate, p.bph.sampleRate, streamer)
	}
	p.bph.ctrl.Streamer = s
	p.mu.Unlock()

	p.send(Event{State: "loading", Path: pb.path, DurSec: pb.dur})

	done := make(chan struct{})
	speaker.Play(beep.Seq(p.bph.ctrl, beep.Callback(func() {
		close(done)
	})))
	p.send(Event{State: "playing", Path: pb.path, DurSec: pb.dur})

	select {
	case <-done:
	case <-pb.ctx.Done():
	}

	cmd.Wait()
	p.mu.Lock()
	if p.bph.streamer == streamer {
		p.bph.streamer.Close()
		p.bph.streamer = nil
		p.bph.ctrl = nil
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
