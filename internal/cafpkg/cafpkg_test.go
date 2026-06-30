package cafpkg

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnarleyburns/parso-pdaudio/internal/ffmpeg"
)

func probeCodec(t *testing.T, ffprobe, path string) string {
	t.Helper()
	out, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=codec_name", "-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}

// TestGoPackagerProducesOpusCaf validates the pure-Go packager (the primary,
// iOS-safe path per spec §7.3) yields a genuine opus-in-CAF that ffprobe reads
// as codec_name=opus (acceptance criterion #2). The ffmpeg fallback is also
// exercised but tolerated-as-unsupported on newer ffmpeg builds, which is the
// exact reason the Go packager is the default.
func TestGoPackagerProducesOpusCaf(t *testing.T) {
	tools := ffmpeg.Detect()
	if !tools.Available() {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	wav := filepath.Join(dir, "t.wav")
	opus := filepath.Join(dir, "t.opus")

	if out, err := exec.Command(tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-ac", "2", wav).CombinedOutput(); err != nil {
		t.Fatalf("tone: %v %s", err, out)
	}
	if err := tools.ToOpus(ctx, wav, opus, "pcm_s16le", 96); err != nil {
		t.Fatalf("opus: %v", err)
	}

	// Primary: pure-Go packager MUST work everywhere.
	goPkg, _ := New("go", tools)
	goCaf := filepath.Join(dir, "go.caf")
	if err := goPkg.Package(ctx, opus, goCaf); err != nil {
		t.Fatalf("go package: %v", err)
	}
	if c := probeCodec(t, tools.FFprobe, goCaf); c != "opus" {
		t.Fatalf("go packager caf codec = %q, want opus", c)
	}

	// Fallback: ffmpeg packager; not implemented in some ffmpeg builds (>=8).
	ffPkg, _ := New("ffmpeg", tools)
	ffCaf := filepath.Join(dir, "ff.caf")
	if err := ffPkg.Package(ctx, opus, ffCaf); err != nil {
		t.Logf("ffmpeg packager unsupported on this build (expected; use go): %v", err)
		return
	}
	if c := probeCodec(t, tools.FFprobe, ffCaf); c != "opus" {
		t.Fatalf("ffmpeg packager caf codec = %q, want opus", c)
	}
}
