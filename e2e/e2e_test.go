//go:build e2e

// Package e2e contains live, network-dependent end-to-end tests that verify
// every source actually enumerates real recordings and that a downloaded track
// converts, packages, and yields a genuinely playable opus-in-CAF.
//
// Run with:  go test -tags e2e ./e2e/...    (optionally PARSO_E2E_PLAY=1)
package e2e

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/johnarleyburns/parso-pdaudio/internal/cafpkg"
	"github.com/johnarleyburns/parso-pdaudio/internal/config"
	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"github.com/johnarleyburns/parso-pdaudio/internal/db"
	"github.com/johnarleyburns/parso-pdaudio/internal/ffmpeg"
	"github.com/johnarleyburns/parso-pdaudio/internal/pipeline"
	"github.com/johnarleyburns/parso-pdaudio/internal/player"
	"github.com/johnarleyburns/parso-pdaudio/internal/provider"
	"github.com/johnarleyburns/parso-pdaudio/internal/sources"
)

const ua = "parso-pdaudio-e2e/1.0 (+https://github.com/johnarleyburns/parso-pdaudio)"

var prefer = provider.FormatRank([]string{"opus", "ogg", "wav", "mp3"})

// TestDiscoverEachSource confirms each source enumerates real audio. Space
// Force is allowed to be empty (spec §2) but must not error.
func TestDiscoverEachSource(t *testing.T) {
	client := provider.NewClient(ua)
	provs, err := sources.Build(sources.Keys(), client, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range provs {
		p := p
		t.Run(p.Key(), func(t *testing.T) {
			if p.Key() == "commons_classical" {
				t.Skip("classical categories traversal requires live network (>129 composers); skip in quick e2e")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			cands, _, _, err := p.Discover(ctx, "")
			if err != nil {
				t.Fatalf("discover %s: %v", p.Key(), err)
			}
			playable := 0
			for _, c := range cands {
				if _, ok := provider.SelectBest(c.Files, prefer, false); ok {
					playable++
				}
			}
			t.Logf("source %-16s candidates=%d playable=%d", p.Key(), len(cands), playable)
			if p.Key() == "spaceforce" {
				return // ~empty by design
			}
			if playable == 0 {
				t.Fatalf("source %s returned no playable candidates", p.Key())
			}
		})
	}
}

// TestSingleTrackPipelinePerSource downloads ONE real track per source and
// runs the convert+package pipeline, proving a playable opus-in-CAF.
func TestSingleTrackPipelinePerSource(t *testing.T) {
	tools := ffmpeg.Detect()
	if !tools.Available() {
		t.Skip("ffmpeg/ffprobe required")
	}
	pkg, _ := cafpkg.New("go", tools)
	client := provider.NewClient(ua)
	provs, _ := sources.Build(sources.Keys(), client, nil)

	for _, p := range provs {
		p := p
		t.Run(p.Key(), func(t *testing.T) {
			if p.Key() == "commons_classical" {
				t.Skip("classical categories traversal requires live network; skip in quick e2e")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			cands, _, _, err := p.Discover(ctx, "")
			if err != nil {
				t.Fatalf("discover: %v", err)
			}
			var file core.CandidateFile
			found := false
			for _, c := range cands {
				if f, ok := provider.SelectBest(c.Files, prefer, false); ok {
					file = f
					found = true
					break
				}
			}
			if !found {
				if p.Key() == "spaceforce" {
					t.Skip("no candidates (expected for spaceforce)")
				}
				t.Fatalf("no selectable candidate for %s", p.Key())
			}

			dir := t.TempDir()
			src := filepath.Join(dir, "src."+file.Format)
			if err := download(ctx, file.URL, src); err != nil {
				t.Fatalf("download %s: %v", file.URL, err)
			}
			codec, dur, err := tools.Probe(ctx, src)
			if err != nil {
				t.Fatalf("probe: %v", err)
			}
			opus := filepath.Join(dir, "t.opus")
			if err := tools.ToOpus(ctx, src, opus, codec, 128); err != nil {
				t.Fatalf("to opus: %v", err)
			}
			caf := filepath.Join(dir, "t.caf")
			if err := pkg.Package(ctx, opus, caf); err != nil {
				t.Fatalf("package: %v", err)
			}
			cc, _, _ := tools.Probe(ctx, caf)
			if cc != "opus" {
				t.Fatalf("caf codec = %q, want opus", cc)
			}
			t.Logf("%-16s OK: %s (%s, %.0fs) -> opus-in-caf", p.Key(), file.URL, codec, dur)

			if os.Getenv("PARSO_E2E_PLAY") != "" {
				pl := player.New()
				if pl.Available() {
					pl.Play(caf)
					time.Sleep(3 * time.Second)
					pl.Stop()
				}
			}
		})
	}
}

// TestEngineEndToEndChopin runs the full DB-backed engine on a few Chopin
// tracks: discover -> download -> convert -> package -> clean, then verifies
// done rows, opus+caf artifacts, codec=opus, no leftover .src.*, and surviving
// provenance (acceptance criteria 1,2,7,9).
func TestEngineEndToEndChopin(t *testing.T) {
	tools := ffmpeg.Detect()
	if !tools.Available() {
		t.Skip("ffmpeg/ffprobe required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	dir := t.TempDir()
	cfg, err := config.Parse([]string{"--dir", dir, "--sources", "chopin",
		"--dl-workers", "4", "--conv-workers", "2", "--pkg-workers", "2", "--clean-workers", "1"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	client := provider.NewClient(ua)
	provs, _ := sources.Build([]string{"chopin"}, client, nil)
	eng, err := pipeline.NewEngine(cfg, store, provs)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Discover(ctx); err != nil {
		t.Fatalf("discover: %v", err)
	}

	// Cap to 3 tracks: skip the rest so the test stays quick.
	if _, err := store.SQL().ExecContext(ctx,
		`UPDATE tracks SET status='skipped'
		 WHERE source='chopin' AND status='discovered'
		   AND id NOT IN (SELECT id FROM tracks WHERE source='chopin' AND status='discovered'
		                  ORDER BY created_at LIMIT 3)`); err != nil {
		t.Fatal(err)
	}

	eng.StartWorkers(ctx)
	defer eng.Stop()

	deadline := time.After(5 * time.Minute)
	for {
		pending, err := eng.Pending(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timeout; %d still pending", pending)
		case <-time.After(time.Second):
		}
	}

	done, err := store.ListPlayable(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) < 3 {
		t.Fatalf("expected 3 done tracks, got %d", len(done))
	}
	for _, tr := range done {
		opusP := filepath.Join(dir, tr.OpusPath)
		cafP := filepath.Join(dir, tr.CafPath)
		if !nonEmpty(opusP) || !nonEmpty(cafP) {
			t.Fatalf("missing artifacts for %s", tr.ID)
		}
		if c, _, _ := tools.Probe(ctx, cafP); c != "opus" {
			t.Fatalf("caf codec = %q for %s, want opus", c, tr.ID)
		}
		// No leftover native source file.
		matches, _ := filepath.Glob(filepath.Join(dir, tr.ID+".src.*"))
		if len(matches) != 0 {
			t.Fatalf("leftover native file(s) %v for %s", matches, tr.ID)
		}
		// Provenance survives.
		if tr.OriginalURL == "" || tr.OriginalSHA1 == "" {
			t.Fatalf("provenance lost for %s", tr.ID)
		}
	}
	t.Logf("engine e2e: %d Chopin tracks fully processed", len(done))
}

func download(ctx context.Context, url, dst string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", ua)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func nonEmpty(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Size() > 0
}
