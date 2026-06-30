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
	"chopin", "beethoven_pitman", "marine", "army",
	"navy", "airforce", "coastguard", "spaceforce",
}

type spec struct {
	provider   string
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
	"beethoven_pitman": {
		provider: "commons", commonsQ: `Beethoven Sonata Musopen`,
		note: "Paul Pitman 32 sonatas; cross-source Musopen set via Commons",
	},
	"marine":     {provider: "commons", commonsQ: `"United States Marine Band"`, note: "PD-USGov mostly"},
	"army":       {provider: "commons", commonsQ: `"United States Army Band"`, note: "PD-USGov mostly"},
	"navy":       {provider: "commons", commonsQ: `"United States Navy Band"`, note: "PD-USGov mostly"},
	"airforce":   {provider: "commons", commonsQ: `"United States Air Force Band"`, note: "PD-USGov mostly"},
	"coastguard": {provider: "commons", commonsQ: `"United States Coast Guard Band"`, note: "PD-USGov mostly"},
	"spaceforce": {provider: "commons", commonsQ: `"United States Space Force Band"`, note: "~empty; founded 2020"},
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

// Build constructs Provider instances for the given source keys.
func Build(keys []string, client *provider.Client) ([]provider.Provider, error) {
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
			out = append(out, &provider.CommonsProvider{
				SourceKey: k,
				Query:     s.commonsQ,
				PageSize:  50,
				Client:    client,
			})
		default:
			return nil, fmt.Errorf("source %q: bad provider %q", k, s.provider)
		}
	}
	return out, nil
}
