// Package ffmpeg wraps the external ffmpeg/ffprobe binaries.
package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Tools reports which external binaries are available.
type Tools struct {
	FFmpeg  string
	FFprobe string
}

// Detect locates ffmpeg and ffprobe on PATH.
func Detect() Tools {
	t := Tools{}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		t.FFmpeg = p
	}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		t.FFprobe = p
	}
	return t
}

// Available reports whether both ffmpeg and ffprobe were found.
func (t Tools) Available() bool { return t.FFmpeg != "" && t.FFprobe != "" }

// Probe returns the first audio stream codec name and the format duration.
func (t Tools) Probe(ctx context.Context, in string) (codec string, durationSec float64, err error) {
	codec, err = t.run(ctx, t.FFprobe,
		"-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=codec_name",
		"-of", "default=noprint_wrappers=1:nokey=1", in)
	if err != nil {
		return "", 0, fmt.Errorf("ffprobe codec: %w", err)
	}
	codec = strings.TrimSpace(codec)

	durStr, err := t.run(ctx, t.FFprobe,
		"-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", in)
	if err != nil {
		return codec, 0, fmt.Errorf("ffprobe duration: %w", err)
	}
	durStr = strings.TrimSpace(durStr)
	if durStr != "" && durStr != "N/A" {
		durationSec, _ = strconv.ParseFloat(durStr, 64)
	}
	return codec, durationSec, nil
}

// ToOpus converts src to an Ogg Opus file at out. If the source is already
// opus it is stream-copied losslessly; otherwise it is encoded with libopus.
func (t Tools) ToOpus(ctx context.Context, src, out, srcCodec string, bitrateK int) error {
	var args []string
	if srcCodec == "opus" {
		args = []string{"-hide_banner", "-loglevel", "error", "-y",
			"-i", src, "-map_metadata", "0", "-c:a", "copy", out}
	} else {
		args = []string{"-hide_banner", "-loglevel", "error", "-y",
			"-i", src, "-map_metadata", "0",
			"-c:a", "libopus", "-b:a", strconv.Itoa(bitrateK) + "k", "-vbr", "on", out}
	}
	if _, err := t.run(ctx, t.FFmpeg, args...); err != nil {
		return fmt.Errorf("ffmpeg opus: %w", err)
	}
	return nil
}

// ToCaf packages an opus file into a CAF via lossless stream copy.
func (t Tools) ToCaf(ctx context.Context, opus, out string) error {
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-i", opus, "-c:a", "copy", out}
	if _, err := t.run(ctx, t.FFmpeg, args...); err != nil {
		return fmt.Errorf("ffmpeg caf: %w", err)
	}
	return nil
}

func (t Tools) run(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s: %s", err, msg)
	}
	return stdout.String(), nil
}
