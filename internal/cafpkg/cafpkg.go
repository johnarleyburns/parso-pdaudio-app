// Package cafpkg packages an Opus file into an iOS-ready CAF container.
package cafpkg

import (
	"context"
	"fmt"

	caf "github.com/nabil6391/opus_caf_converter/caf"

	"github.com/johnarleyburns/parso-pdaudio/internal/ffmpeg"
)

// Packager turns an .opus file into a sibling .caf.
type Packager interface {
	Package(ctx context.Context, opusPath, cafPath string) error
	Name() string
}

// GoPackager uses the pure-Go opus->caf converter (iOS-safe framing).
type GoPackager struct{}

// Name implements Packager.
func (GoPackager) Name() string { return "go" }

// Package implements Packager.
func (GoPackager) Package(_ context.Context, opusPath, cafPath string) error {
	if err := caf.ConvertOpusToCaf(opusPath, cafPath); err != nil {
		return fmt.Errorf("go caf: %w", err)
	}
	return nil
}

// FFmpegPackager uses ffmpeg's lossless stream copy as a fallback.
type FFmpegPackager struct{ Tools ffmpeg.Tools }

// Name implements Packager.
func (FFmpegPackager) Name() string { return "ffmpeg" }

// Package implements Packager.
func (p FFmpegPackager) Package(ctx context.Context, opusPath, cafPath string) error {
	return p.Tools.ToCaf(ctx, opusPath, cafPath)
}

// New returns the packager selected by name ("go" | "ffmpeg").
func New(name string, tools ffmpeg.Tools) (Packager, error) {
	switch name {
	case "go":
		return GoPackager{}, nil
	case "ffmpeg":
		return FFmpegPackager{Tools: tools}, nil
	default:
		return nil, fmt.Errorf("unknown packager %q", name)
	}
}
