package player

import (
	"encoding/binary"
	"math"
	"os"
	"testing"
	"time"
)

const (
	testSampleRate = 44100
	testDuration   = 2.0
)

func makeSynthWAV(t *testing.T, path string) {
	t.Helper()
	numSamples := int(testSampleRate * testDuration)
	dataSize := numSamples * 4
	fileSize := 44 + dataSize

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	buf := make([]byte, 44)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(fileSize-8))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(buf[22:24], 2) // stereo
	binary.LittleEndian.PutUint32(buf[24:28], testSampleRate)
	binary.LittleEndian.PutUint32(buf[28:32], testSampleRate*4)
	binary.LittleEndian.PutUint16(buf[32:34], 4)
	binary.LittleEndian.PutUint16(buf[34:36], 16)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataSize))
	if _, err := f.Write(buf); err != nil {
		t.Fatal(err)
	}

	sampleData := make([]byte, dataSize)
	for i := 0; i < numSamples; i++ {
		ti := float64(i) / float64(testSampleRate)
		v := int16(0.3 * math.MaxInt16 * math.Sin(2*math.Pi*440*ti))
		binary.LittleEndian.PutUint16(sampleData[i*4:i*4+2], uint16(v))
		binary.LittleEndian.PutUint16(sampleData[i*4+2:i*4+4], uint16(v))
	}
	if _, err := f.Write(sampleData); err != nil {
		t.Fatal(err)
	}
}

// TestPlayFlowEvents verifies the full Player API sends correct state
// transitions: loading → playing → (ended or error).
func TestPlayFlowEvents(t *testing.T) {
	p := New()
	if !p.Available() {
		t.Skip("no audio backend available")
	}
	if p.Backend() == "beep" && raceDetector {
		t.Skip("skipping beep under -race (oto/v3 internal race on darwin)")
	}

	path := t.TempDir() + "/synth.wav"
	makeSynthWAV(t, path)

	p.Play(path)

	var gotLoading, gotPlaying, gotEnded bool
	timeout := time.After(20 * time.Second)
	for {
		select {
		case ev := <-p.Events():
			t.Logf("event: state=%s ended=%v err=%q dur=%.2f",
				ev.State, ev.Ended, ev.Err, ev.DurSec)
			if ev.State == "loading" {
				gotLoading = true
			}
			if ev.State == "playing" && !ev.Ended {
				gotPlaying = true
				if p.Backend() == "beep" {
					// Position should start advancing.
					time.Sleep(500 * time.Millisecond)
					pos, dur := p.Position()
					t.Logf("mid-playback position: %.2fs / %.2fs", pos, dur)
					if pos <= 0 && dur > 0 {
						t.Error("position not advancing during playback")
					}
				}
			}
			if ev.Ended {
				gotEnded = true
				if ev.Err != "" {
					t.Logf("ended with error (OK on headless CI): %s", ev.Err)
				}
				goto done
			}
		case <-timeout:
			t.Error("timeout waiting for ended event")
			goto done
		}
	}
done:
	p.Stop()
	if !gotLoading {
		t.Error("never received 'loading' event")
	}
	if !gotPlaying {
		t.Error("never received 'playing' event")
	}
	if !gotEnded {
		t.Error("never received 'ended' event")
	}
}

func TestPreferredPath(t *testing.T) {
	// beep prefers opus over caf when both exist.
	dir := t.TempDir()
	opus := dir + "/a.opus"
	caf := dir + "/a.caf"
	os.WriteFile(opus, []byte("x"), 0644)
	os.WriteFile(caf, []byte("x"), 0644)

	bp := &Player{backend: "beep"}
	if got := bp.PreferredPath(dir, "a.opus", "a.caf"); got != opus {
		t.Fatalf("beep should prefer opus, got %q", got)
	}
	os.Remove(opus)
	if got := bp.PreferredPath(dir, "", "a.caf"); got != caf {
		t.Fatalf("beep no-opus should pick caf, got %q", got)
	}

	// afplay prefers caf over opus.
	os.WriteFile(opus, []byte("x"), 0644)
	ap := &Player{backend: "afplay"}
	if got := ap.PreferredPath(dir, "a.opus", "a.caf"); got != caf {
		t.Fatalf("afplay should prefer caf, got %q", got)
	}
	os.Remove(caf)
	if got := ap.PreferredPath(dir, "a.opus", ""); got != opus {
		t.Fatalf("afplay should fall back to opus, got %q", got)
	}

	// ffplay prefers opus.
	pf := &Player{backend: "ffplay"}
	if got := pf.PreferredPath(dir, "a.opus", "a.caf"); got != opus {
		t.Fatalf("ffplay should prefer opus, got %q", got)
	}
}

func TestNoBackendEmitsError(t *testing.T) {
	p := &Player{events: make(chan Event, 4)}
	if p.Available() {
		t.Skip("player has backend; test requires no backend")
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
	p.Play("/dev/null")
	p.Stop()
}
