//go:build darwin

package player

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/speaker"
)

const (
	beepSampleRate = beep.SampleRate(44100)
	beepFormat     = 2 // stereo channels
	beepPrecision  = 2 // 16-bit = 2 bytes
)

type beepHandle struct {
	streamer *pcmStreamer
	ctrl     *beep.Ctrl
}

// Package-level speaker init (once per process, mutex-serialized).
var (
	beepInitOnce sync.Once
	beepInitErr  error
	beepInitMu   sync.Mutex
)

// pcmStreamer wraps a raw s16le PCM reader as a beep.Streamer.
type pcmStreamer struct {
	r   io.Reader
	buf []byte
	pos int // samples consumed
	err error
}

func (s *pcmStreamer) Stream(samples [][2]float64) (n int, ok bool) {
	if s.err != nil {
		return 0, false
	}
	needed := len(samples) * 4 // 2 channels × 2 bytes
	if cap(s.buf) < needed {
		s.buf = make([]byte, needed)
	}
	s.buf = s.buf[:needed]
	read, err := io.ReadFull(s.r, s.buf)
	n = read / 4
	for i := 0; i < n; i++ {
		l := int16(binary.LittleEndian.Uint16(s.buf[i*4 : i*4+2]))
		r := int16(binary.LittleEndian.Uint16(s.buf[i*4+2 : i*4+4]))
		samples[i][0] = float64(l) / 32768.0
		samples[i][1] = float64(r) / 32768.0
	}
	s.pos += n
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		s.err = io.EOF
		return n, false
	}
	if err != nil {
		s.err = err
		return n, false
	}
	return n, true
}

func (s *pcmStreamer) Err() error {
	if s.err == io.EOF {
		return nil
	}
	return s.err
}

func (s *pcmStreamer) Position() int {
	return s.pos
}

func (s *pcmStreamer) Len() int {
	return -1 // unknown (pipe)
}

func (s *pcmStreamer) Seek(p int) error {
	return fmt.Errorf("pipe is not seekable")
}

func (s *pcmStreamer) Close() error {
	return nil
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
	if p.bph.streamer == nil {
		return
	}
	delta := int(relSec * float64(beepSampleRate))
	speaker.Lock()
	newPos := p.bph.streamer.Position() + delta
	if newPos < 0 {
		newPos = 0
	}
	if err := p.bph.streamer.Seek(newPos); err != nil {
		// pipe is not seekable — reset via ffmpeg seek
	}
	speaker.Unlock()
}

func (p *Player) Position() (pos, dur float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bph.streamer == nil {
		if p.playing != nil {
			return 0, p.playing.dur
		}
		return 0, 0
	}
	speaker.Lock()
	spos := p.bph.streamer.Position()
	speaker.Unlock()
	pos = float64(spos) / float64(beepSampleRate)
	if p.playing != nil {
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
	// Raw PCM output from ffmpeg (no WAV header — wav.Decode has pipe issues).
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-i", pb.path,
		"-f", "s16le", "-acodec", "pcm_s16le",
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

	streamer := &pcmStreamer{r: stdout}

	p.mu.Lock()
	p.bph.streamer = streamer
	p.bph.ctrl = &beep.Ctrl{Streamer: streamer, Paused: false}
	p.mu.Unlock()

	beepInitOnce.Do(func() {
		beepInitMu.Lock()
		defer beepInitMu.Unlock()
		bufSize := beepSampleRate.N(time.Second / 10)
		beepInitErr = speaker.Init(beepSampleRate, bufSize)
	})
	if beepInitErr != nil {
		p.send(Event{Path: pb.path, Err: fmt.Sprintf("audio init: %v", beepInitErr), Ended: true})
		cmd.Process.Kill()
		cmd.Wait()
		return
	}

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
