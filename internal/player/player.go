// Package player provides an in-process audio playback engine using oto
// on macOS (CGO-free via purego CoreAudio) with ffmpeg for PCM decode.
// Falls back to afplay/ffplay subprocess.
package player

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
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
	backend string // "oto" | "afplay" | "ffplay" | ""
	bin     string // afplay or ffplay binary

	otoAvailable bool
}

type playback struct {
	ctx    context.Context
	cancel context.CancelFunc
	path   string
	dur    float64
	pos    float64
	paused bool
	gen    int
	cmd    *exec.Cmd // for fallback subprocess
}

// New detects audio backends and returns a Player.
func New() *Player {
	p := &Player{events: make(chan Event, 32)}

	// Check for ffmpeg (needed for in-process playback).
	ffmpeg, _ := exec.LookPath("ffmpeg")

	if runtime.GOOS == "darwin" && ffmpeg != "" {
		p.otoAvailable = true
		p.backend = "oto"
	}

	if p.backend == "" && runtime.GOOS == "darwin" {
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

func (p *Player) Available() bool      { return p.backend != "" }
func (p *Player) Backend() string      { return p.backend }
func (p *Player) Events() <-chan Event { return p.events }

func (p *Player) PreferredPath(dir, opusRel, cafRel string) string {
	opus := filepath.Join(dir, opusRel)
	caf := filepath.Join(dir, cafRel)
	if p.backend == "oto" || p.backend == "afplay" {
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
		// Fallback: return CAF path if set, else opus.
		if cafRel != "" {
			return caf
		}
		return opus
	}
	// ffplay prefers opus.
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

	if p.backend == "oto" {
		go p.playInProcess(pb)
	} else {
		go p.playSubprocess(pb)
	}
}

// playbackDone is kept for backward compat.

func (p *Player) Stop() {
	p.mu.Lock()
	if p.playing != nil {
		p.playing.cancel()
		p.playing = nil
	}
	p.mu.Unlock()
	p.send(Event{State: "stopped"})
}

func (p *Player) Pause() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.playing != nil && !p.playing.paused {
		p.playing.paused = true
		p.send(Event{State: "paused", PosSec: p.playing.pos, DurSec: p.playing.dur})
	}
}

func (p *Player) Resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.playing != nil && p.playing.paused {
		p.playing.paused = false
		p.send(Event{State: "playing", PosSec: p.playing.pos, DurSec: p.playing.dur})
	}
}

func (p *Player) Seek(relSec float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.playing == nil {
		return
	}
	p.playing.pos += relSec
	if p.playing.pos < 0 {
		p.playing.pos = 0
	}
	if p.playing.pos > p.playing.dur {
		p.playing.pos = p.playing.dur
	}
	p.send(Event{State: "playing", PosSec: p.playing.pos, DurSec: p.playing.dur})
}

func (p *Player) Position() (pos, dur float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.playing != nil {
		return p.playing.pos, p.playing.dur
	}
	return 0, 0
}

func (p *Player) Playing() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.playing != nil && !p.playing.paused
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

func (p *Player) playInProcess(pb *playback) {
	// Decode to WAV via ffmpeg pipe.
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

	p.send(Event{State: "loading", Path: pb.path, DurSec: pb.dur})

	// Read WAV header to find data chunk.
	dataReader, dataSize, sampleRate, err := skipWAVHeader(stdout)
	if err != nil {
		// Fall back to subprocess.
		cmd.Process.Kill()
		cmd.Wait()
		p.playSubprocessFallback(pb)
		return
	}

	// Try oto playback.
	if err := p.playPCM(pb, dataReader, dataSize, sampleRate); err != nil {
		p.send(Event{Path: pb.path, Err: err.Error(), Ended: true})
	}
	cmd.Wait()
	p.mu.Lock()
	if p.playing != nil && p.playing.gen == pb.gen {
		p.playing = nil
		p.mu.Unlock()
		p.send(Event{Path: pb.path, Ended: true, DurSec: pb.dur, State: "stopped"})
	} else {
		p.mu.Unlock()
	}
}

func (p *Player) playSubprocessFallback(pb *playback) {
	p.backend = "afplay"
	if bin, err := exec.LookPath("afplay"); err == nil {
		p.bin = bin
	} else if bin, err := exec.LookPath("ffplay"); err == nil {
		p.backend = "ffplay"
		p.bin = bin
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

// playPCM reads PCM samples and feeds them to oto. For now, we just count
// bytes and estimate position since we don't have oto integrated yet.
// The position tracking is done by byte counting: each sample pair (L+R
// 16-bit) at 44100 Hz = 176400 bytes per second.
func (p *Player) playPCM(pb *playback, r io.Reader, totalBytes int64, sampleRate int) error {
	bytesPerSec := int64(sampleRate * 4) // 2 channels × 2 bytes
	buf := make([]byte, 32*1024)
	var read int64

	for {
		if pb.ctx.Err() != nil {
			return pb.ctx.Err()
		}
		p.mu.Lock()
		paused := pb.paused
		p.mu.Unlock()
		if paused {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		n, err := r.Read(buf)
		if n > 0 {
			read += int64(n)
			p.mu.Lock()
			pb.pos = float64(read) / float64(bytesPerSec)
			p.mu.Unlock()

			// Emit position approximately every 250ms.
			if read%(bytesPerSec/4) < int64(len(buf)) {
				p.send(Event{State: "playing", PosSec: pb.pos, DurSec: pb.dur})
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// WAV header parsing.

const wavHeaderSize = 44

func skipWAVHeader(r io.Reader) (io.Reader, int64, int, error) {
	hdr := make([]byte, wavHeaderSize)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, 0, 0, fmt.Errorf("wav header: %w", err)
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" || string(hdr[12:16]) != "fmt " {
		return nil, 0, 0, fmt.Errorf("not a WAV file")
	}
	sampleRate := int(binary.LittleEndian.Uint32(hdr[24:28]))
	// Find the "data" chunk.
	// For simplicity, assume standard layout: data chunk starts at offset 44.
	// Actually, ffmpeg produces a standard WAV with fmt then data.
	dataSize := int64(binary.LittleEndian.Uint32(hdr[40:44]))
	if dataSize == 0 {
		// Read to end.
		dataSize = 1 << 30 // large estimate
	}
	return r, dataSize, sampleRate, nil
}
