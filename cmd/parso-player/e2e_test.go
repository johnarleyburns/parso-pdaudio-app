package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/johnarleyburns/parso-pdaudio/internal/db"
	"github.com/johnarleyburns/parso-pdaudio/internal/player"
	"github.com/johnarleyburns/parso-pdaudio/internal/r2"
)

// TestE2ELivePlayer exercises the real read-only consumer path against R2:
// download the published DB, search, browse the work tree, presign+fetch a
// track's CAF, and actually start playback. It is gated on PARSO_E2E=1 (and
// working R2 read credentials) so the default `go test ./...` stays hermetic.
func TestE2ELivePlayer(t *testing.T) {
	if os.Getenv("PARSO_E2E") == "" {
		t.Skip("set PARSO_E2E=1 to run the live R2 e2e test")
	}
	cfg, err := r2.LoadConfigMode(r2.ModeReadOnly)
	if err != nil {
		t.Skipf("no R2 read config: %v", err)
	}
	client := r2.New(cfg)
	ctx := context.Background()

	// 1. Download the distribution DB from R2 and open it.
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "library.db")
	if err := client.GetToFile(ctx, "db/library.db", dbPath); err != nil {
		t.Fatalf("download DB: %v", err)
	}
	store, err := db.Open(tmp)
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	defer store.Close()

	// 2. Search.
	hits, err := store.Search(ctx, "chopin", true, 20)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("search 'chopin' returned no playable tracks")
	}
	t.Logf("search 'chopin' -> %d tracks (e.g. %q)", len(hits), hits[0].DisplayTitleText)

	// 3. Browse the full tree: source -> composer -> work -> movements.
	sources, err := store.BrowseSources(ctx)
	if err != nil || len(sources) == 0 {
		t.Fatalf("browse sources: %v (n=%d)", err, len(sources))
	}
	var playable *coreTrack
	for _, s := range sources {
		comps, err := store.BrowseComposers(ctx, s.Key)
		if err != nil || len(comps) == 0 {
			continue
		}
		works, err := store.BrowseWorks(ctx, s.Key, comps[0].Key)
		if err != nil || len(works) == 0 {
			continue
		}
		tracks, err := store.WorkTracks(ctx, works[0].Key, 10)
		if err != nil || len(tracks) == 0 {
			continue
		}
		playable = &coreTrack{ID: tracks[0].ID, CafPath: tracks[0].CafPath, Title: tracks[0].DisplayTitleText}
		t.Logf("browsed %s > %s > %s -> %d movements", s.Key, comps[0].Name, works[0].Name, len(tracks))
		break
	}
	if playable == nil {
		t.Fatal("browse tree yielded no playable track")
	}

	// 4. Presign the track URL and confirm the CAF is reachable.
	url, err := client.PresignGet("audio/"+playable.ID+".caf", 10*time.Minute)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=0-1023")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetch audio: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("audio HTTP %d", resp.StatusCode)
	}

	// 5. Play: download the CAF and start playback, asserting the player reaches
	//    a playing/loading state, then stop.
	local := filepath.Join(tmp, playable.ID+".caf")
	if err := client.GetToFile(ctx, "audio/"+playable.ID+".caf", local); err != nil {
		t.Fatalf("download CAF: %v", err)
	}
	pl := player.New()
	if !pl.Available() {
		t.Log("no audio backend on this host; playback start skipped (fetch verified)")
		return
	}
	pl.Play(local)
	defer pl.Stop()
	select {
	case ev := <-pl.Events():
		if ev.Err != "" {
			t.Fatalf("playback error: %s", ev.Err)
		}
		t.Logf("playback started: state=%q track=%q", ev.State, playable.Title)
	case <-time.After(15 * time.Second):
		t.Fatal("player did not start within 15s")
	}
}

// coreTrack is a tiny local holder to avoid importing core just for a struct.
type coreTrack struct {
	ID      string
	CafPath string
	Title   string
}
