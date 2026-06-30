// Package config defines runtime configuration parsed from CLI flags.
package config

import (
	"flag"
	"fmt"
	"runtime"
	"strings"
)

// Config holds all tunable settings for a run.
type Config struct {
	Dir                     string
	Sources                 []string
	Prefer                  []string
	AllowFallback           bool
	RequireLicense          []string
	OpusBitrate             int
	Packager                string
	KeepSource              bool
	DLWorkers               int
	ConvWorkers             int
	PkgWorkers              int
	CleanWorkers            int
	MaxAttempts             int
	MaxTracks               int
	CommonsConcurrency      int
	CommonsReqPerSec        float64
	CommonsAllowAttribution bool
	CommonsAllowFlac        bool
	MinDurationSec          float64
	ResetSkipped            bool
	NoTUI                   bool
	PlayOnly                bool
	UserAgent               string
}

const defaultUserAgent = "parso-pdaudio/1.0 (+https://github.com/johnarleyburns/parso-pdaudio)"

// Parse builds a Config from the given args (typically os.Args[1:]).
func Parse(args []string) (*Config, error) {
	fs := flag.NewFlagSet("parso-pdaudio", flag.ContinueOnError)

	var (
		dir              = fs.String("dir", "./library", "output directory (DB + media)")
		sources          = fs.String("sources", "all", "comma list of source keys or \"all\"")
		prefer           = fs.String("prefer", "opus,ogg,wav,mp3", "format preference, high->low")
		allowFB          = fs.Bool("allow-fallback", false, "if no preferred format, take any audio")
		reqLicense       = fs.String("require-license", "", "license allowlist; skip others; \"\" = allow all")
		bitrate          = fs.Int("opus-bitrate", 128, "libopus VBR target kbps")
		packager         = fs.String("packager", "go", "caf packager: go | ffmpeg")
		keepSrc          = fs.Bool("keep-source", false, "don't delete native after packaging")
		dlW              = fs.Int("dl-workers", 4, "initial download pool size")
		convW            = fs.Int("conv-workers", 2, "initial convert pool size")
		pkgW             = fs.Int("pkg-workers", 1, "initial package pool size")
		cleanW           = fs.Int("clean-workers", 1, "initial cleaner pool size")
		maxAtt           = fs.Int("max-attempts", 3, "per-stage retry ceiling")
		maxTracks        = fs.Int("max-tracks", 0, "cap total tracks processed this run (0 = unlimited)")
		commonsConc      = fs.Int("commons-concurrency", 2, "max parallel Commons GETs")
		commonsRPS       = fs.Float64("commons-rate", 0.5, "Commons download requests per second per host")
		commonsAllowAttr = fs.Bool("commons-allow-attribution", false, "allow CC-BY/SA/OAL licenses (classical src)")
		commonsAllowFlac = fs.Bool("commons-allow-flac", true, "allow FLAC downloads (classical src); default on")
		minDuration      = fs.Float64("min-duration", 30, "skip tracks shorter than N seconds after ffprobe")
		resetSkip        = fs.Bool("reset-skipped", false, "reset cap-skipped rows to discovered on startup")
		noTUI            = fs.Bool("no-tui", false, "headless mode (log progress)")
		playOnly         = fs.Bool("play", false, "open the player UI only (no pipeline)")
		ua               = fs.String("user-agent", defaultUserAgent, "HTTP User-Agent")
	)

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	cfg := &Config{
		Dir:                     *dir,
		Prefer:                  splitCSV(*prefer),
		AllowFallback:           *allowFB,
		RequireLicense:          splitCSV(strings.ToLower(*reqLicense)),
		OpusBitrate:             *bitrate,
		Packager:                *packager,
		KeepSource:              *keepSrc,
		DLWorkers:               *dlW,
		ConvWorkers:             *convW,
		PkgWorkers:              *pkgW,
		CleanWorkers:            *cleanW,
		MaxAttempts:             *maxAtt,
		MaxTracks:               *maxTracks,
		CommonsConcurrency:      *commonsConc,
		CommonsReqPerSec:        *commonsRPS,
		CommonsAllowAttribution: *commonsAllowAttr,
		CommonsAllowFlac:        *commonsAllowFlac,
		MinDurationSec:          *minDuration,
		ResetSkipped:            *resetSkip,
		NoTUI:                   *noTUI,
		PlayOnly:                *playOnly,
		UserAgent:               *ua,
	}
	cfg.Sources = splitCSV(*sources)

	if cfg.Packager != "go" && cfg.Packager != "ffmpeg" {
		return nil, fmt.Errorf("invalid --packager %q (want go|ffmpeg)", cfg.Packager)
	}
	if cfg.OpusBitrate < 6 || cfg.OpusBitrate > 510 {
		return nil, fmt.Errorf("invalid --opus-bitrate %d (want 6..510)", cfg.OpusBitrate)
	}
	if cfg.CommonsConcurrency < 1 {
		cfg.CommonsConcurrency = 1
	}
	if cfg.CommonsReqPerSec <= 0 {
		cfg.CommonsReqPerSec = 0.5
	}
	return cfg, nil
}

// MaxFFmpeg bounds total concurrent ffmpeg/ffprobe processes by CPU.
func (c *Config) MaxFFmpeg() int {
	n := runtime.NumCPU()
	if n > 4 {
		n = 4
	}
	if n < 1 {
		n = 1
	}
	return n
}

// LicenseAllowed reports whether a captured license passes the allowlist.
func (c *Config) LicenseAllowed(short, url string) bool {
	if len(c.RequireLicense) == 0 {
		return true
	}
	hay := strings.ToLower(short + " " + url)
	for _, want := range c.RequireLicense {
		if want == "" {
			continue
		}
		switch want {
		case "cc0":
			if strings.Contains(hay, "cc0") || strings.Contains(hay, "publicdomain/zero") {
				return true
			}
		case "pd-usgov":
			if strings.Contains(hay, "usgov") || strings.Contains(hay, "us government") || strings.Contains(hay, "pd-usgov") {
				return true
			}
		case "pd":
			if strings.Contains(hay, "public domain") || strings.Contains(hay, "publicdomain") {
				return true
			}
		default:
			if strings.Contains(hay, want) {
				return true
			}
		}
	}
	return false
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
