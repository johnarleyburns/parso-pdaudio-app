package ffmpeg

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeTone writes a short stereo sine WAV using ffmpeg; skips if unavailable.
func makeTone(t *testing.T, tools Tools, path string) {
	t.Helper()
	cmd := exec.Command(tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-ac", "2", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make tone: %v: %s", err, out)
	}
}

func TestConvertAndPackagePipeline(t *testing.T) {
	tools := Detect()
	if !tools.Available() {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	wav := filepath.Join(dir, "tone.wav")
	opus := filepath.Join(dir, "tone.opus")
	caf := filepath.Join(dir, "tone.caf")
	makeTone(t, tools, wav)

	codec, dur, err := tools.Probe(ctx, wav)
	if err != nil {
		t.Fatalf("probe wav: %v", err)
	}
	if dur < 0.5 || dur > 2 {
		t.Fatalf("unexpected duration %.2f", dur)
	}
	if codec == "opus" {
		t.Fatalf("tone wav should not be opus, got %q", codec)
	}

	if err := tools.ToOpus(ctx, wav, opus, codec, 96); err != nil {
		t.Fatalf("to opus: %v", err)
	}
	if c, _, _ := tools.Probe(ctx, opus); c != "opus" {
		t.Fatalf("opus codec = %q, want opus", c)
	}

	// ffmpeg's opus->caf muxer is unsupported on some newer builds (>=8); the
	// pure-Go packager is the primary path. Tolerate the unsupported case here.
	if err := tools.ToCaf(ctx, opus, caf); err != nil {
		if strings.Contains(err.Error(), "unsupported") || strings.Contains(err.Error(), "not yet implemented") {
			t.Skipf("ffmpeg build cannot mux opus->caf (use go packager): %v", err)
		}
		t.Fatalf("to caf: %v", err)
	}
	// Acceptance criterion #2: ffprobe on the .caf reports codec_name=opus.
	if c, _, _ := tools.Probe(ctx, caf); c != "opus" {
		t.Fatalf("caf codec = %q, want opus", c)
	}
}
