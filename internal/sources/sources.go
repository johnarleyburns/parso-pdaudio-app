// Package sources is the verified registry mapping source keys to providers.
package sources

import (
	"fmt"

	"github.com/johnarleyburns/parso-pdaudio/internal/provider"
)

// Entry describes one source in the manifest.
type Entry struct {
	Key      string
	Provider string // "ia" | "commons"
	Note     string
}

// Order is the canonical display/processing order of sources.
var Order = []string{
	"chopin", "bach_wtc1", "goldberg", "beethoven_pitman", "marine", "army",
	"navy", "airforce", "coastguard", "spaceforce",
	"commons_classical",
}

type spec struct {
	provider   string
	mode       string // for commons: "" = search, "classical_categories" = category traversal
	iaID       string
	iaComposer string
	commonsQ   string
	note       string
}

var manifest = map[string]spec{
	"chopin": {
		provider: "ia", iaID: "musopen-chopin", iaComposer: "Frédéric Chopin",
		note: "CC0; Ogg Vorbis derivatives preferred",
	},
	"bach_wtc1": {
		provider: "ia", iaID: "bach-well-tempered-clavier-book-1", iaComposer: "Johann Sebastian Bach",
		note: "Well-Tempered Clavier Book 1, public domain recordings",
	},
	"goldberg": {
		provider: "ia", iaID: "The_Open_Goldberg_Variations-11823", iaComposer: "Johann Sebastian Bach",
		note: "Open Goldberg Variations, CC0 public domain",
	},
	"beethoven_pitman": {
		provider: "commons", commonsQ: `Beethoven Sonata Musopen`,
		note: "Paul Pitman 32 sonatas; cross-source Musopen set via Commons",
	},
	"marine":            {provider: "commons", commonsQ: `"United States Marine Band"`, note: "PD-USGov mostly"},
	"army":              {provider: "commons", commonsQ: `"United States Army Band"`, note: "PD-USGov mostly"},
	"navy":              {provider: "commons", commonsQ: `"United States Navy Band"`, note: "PD-USGov mostly"},
	"airforce":          {provider: "commons", commonsQ: `"United States Air Force Band"`, note: "PD-USGov mostly"},
	"coastguard":        {provider: "commons", commonsQ: `"United States Coast Guard Band"`, note: "PD-USGov mostly"},
	"spaceforce":        {provider: "commons", commonsQ: `"United States Space Force Band"`, note: "~empty; founded 2020"},
	"commons_classical": {provider: "commons", mode: "classical_categories", note: "~129 composers via category traversal; CC0/PD filtered"},
}

// Keys returns all known source keys in canonical order.
func Keys() []string { return append([]string(nil), Order...) }

// Resolve expands the requested keys ("all" or a list) into concrete keys.
func Resolve(requested []string) ([]string, error) {
	if len(requested) == 0 || (len(requested) == 1 && requested[0] == "all") {
		return Keys(), nil
	}
	var out []string
	for _, k := range requested {
		if _, ok := manifest[k]; !ok {
			return nil, fmt.Errorf("unknown source %q (known: %v)", k, Order)
		}
		out = append(out, k)
	}
	return out, nil
}

// BuildOpts carries optional configuration needed by specific provider modes.
type BuildOpts struct {
	AllowAttribution bool
	AllowFlac        bool
	Prefer           []string
}

// Build constructs Provider instances for the given source keys.
func Build(keys []string, client *provider.Client, opts *BuildOpts) ([]provider.Provider, error) {
	if opts == nil {
		opts = &BuildOpts{}
	}
	var out []provider.Provider
	for _, k := range keys {
		s, ok := manifest[k]
		if !ok {
			return nil, fmt.Errorf("unknown source %q", k)
		}
		switch s.provider {
		case "ia":
			out = append(out, &provider.IAProvider{
				SourceKey:      k,
				Identifier:     s.iaID,
				ComposerForAll: s.iaComposer,
				Client:         client,
			})
		case "commons":
			if s.mode == "classical_categories" {
				policy := "strict"
				if opts.AllowAttribution {
					policy = "attribution"
				}
				pref := opts.Prefer
				if opts.AllowFlac {
					hasFlac := false
					for _, p := range pref {
						if p == "flac" {
							hasFlac = true
							break
						}
					}
					if !hasFlac {
						// insert flac after ogg
						pref = make([]string, 0, len(opts.Prefer)+1)
						for _, p := range opts.Prefer {
							pref = append(pref, p)
							if p == "ogg" {
								pref = append(pref, "flac")
							}
						}
					}
				}
				out = append(out, &provider.ClassicalCategoriesProvider{
					SourceKey:          k,
					RootCategory:       "Category:Audio files of classical music by composer",
					MaxDepth:           3,
					SkipSubcatPatterns: []string{"MIDI files", "Synthesized", "Sheet music", "Scores", "metronome"},
					FormatPreference:   pref,
					MinBytes:           250000,
					MinDurationSec:     30,
					LicensePolicy:      policy,
					ComposerAllowlist:  nil,
					PageSize:           100,
					Client:             client,
				})
			} else {
				out = append(out, &provider.CommonsProvider{
					SourceKey: k,
					Query:     s.commonsQ,
					PageSize:  50,
					Client:    client,
				})
			}
		default:
			return nil, fmt.Errorf("source %q: bad provider %q", k, s.provider)
		}
	}
	return out, nil
}
