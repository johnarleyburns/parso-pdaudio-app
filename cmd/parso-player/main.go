// Command parso-player is a standalone terminal client for a parso library
// published to Cloudflare R2. It downloads the distribution DB, lets the user
// search and browse the work tree, and streams CAF audio from R2 on demand.
//
// It is a read-only consumer of the bucket; it never writes to R2. All R2
// settings are loaded at runtime from the same gitignored secret files/env vars
// used by `parso sync` (see internal/r2).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/johnarleyburns/parso-pdaudio/internal/db"
	"github.com/johnarleyburns/parso-pdaudio/internal/player"
	"github.com/johnarleyburns/parso-pdaudio/internal/r2"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("parso-player", flag.ContinueOnError)
	localDB := fs.String("db", "", "use a local library.db instead of downloading from R2")
	cacheDir := fs.String("cache", defaultCacheDir(), "local cache directory (DB + streamed audio)")
	refresh := fs.Bool("refresh", false, "re-download the DB from R2 even if cached")
	check := fs.Bool("check", false, "headless connectivity check: download DB, browse, presign+fetch a track, then exit")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if err := os.MkdirAll(*cacheDir, 0o755); err != nil {
		return err
	}

	// R2 client is optional when a local DB is supplied, but required to stream
	// audio. We still try to load it so playback works.
	// The player only reads from the bucket → use the read-only credential profile.
	var client *r2.Client
	if cfg, err := r2.LoadConfigMode(r2.ModeReadOnly); err != nil {
		if *localDB == "" {
			return fmt.Errorf("load R2 config: %w", err)
		}
		fmt.Fprintln(os.Stderr, "warning: no R2 config — browsing only, playback disabled:", err)
	} else {
		client = r2.New(cfg)
	}

	// Resolve the DB directory: db.Open opens <dir>/library.db.
	dbDir := *cacheDir
	if *localDB != "" {
		dbDir = filepath.Dir(*localDB)
	} else {
		dst := filepath.Join(*cacheDir, "library.db")
		if _, err := os.Stat(dst); err != nil || *refresh {
			if client == nil {
				return fmt.Errorf("cannot download DB without R2 config")
			}
			fmt.Fprintln(os.Stderr, "downloading library DB from R2...")
			if err := client.GetToFile(context.Background(), "db/library.db", dst); err != nil {
				return fmt.Errorf("download DB: %w", err)
			}
		}
	}

	store, err := db.Open(dbDir)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	audioCache := filepath.Join(*cacheDir, "audio")
	_ = os.MkdirAll(audioCache, 0o755)

	if *check {
		return runCheck(store, client)
	}

	pl := player.New()
	m := newModel(store, pl, client, audioCache)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	pl.Stop()
	return err
}

// runCheck exercises the read-only consumer path headlessly: it confirms the DB
// opened (already downloaded above), browses the work tree, then presigns and
// fetches a real track's CAF from R2 — printing a masked URL (no secrets).
func runCheck(store *db.DB, client *r2.Client) error {
	ctx := context.Background()
	sources, err := store.BrowseSources(ctx)
	if err != nil {
		return err
	}
	var totalWorks, totalTracks int
	for _, s := range sources {
		totalTracks += s.Count
	}
	works, _ := store.SQL().QueryContext(ctx, `SELECT COUNT(*) FROM works`)
	if works != nil {
		for works.Next() {
			_ = works.Scan(&totalWorks)
		}
		works.Close()
	}
	fmt.Printf("DB opened: %d sources, %d works, %d playable tracks\n",
		len(sources), totalWorks, totalTracks)
	if len(sources) > 0 {
		fmt.Printf("  first source: %s (%d)\n", sources[0].Name, sources[0].Count)
	}

	// Probe in upload order (by id) so the check finds an uploaded CAF during a
	// partial sync.
	tracks, err := store.ListCanonicalCAF(ctx)
	if err != nil || len(tracks) == 0 {
		return fmt.Errorf("no playable track found: %v", err)
	}

	if client == nil {
		fmt.Printf("sample track: %s (id=%s)\n", tracks[0].DisplayTitleText, tracks[0].ID)
		fmt.Println("(no R2 client; skipping audio URL)")
		return nil
	}

	// Find a track whose CAF is already uploaded (HEAD-probe candidates), so the
	// check works during a partial sync.
	t := tracks[0]
	found := false
	for _, cand := range tracks {
		exists, _, herr := client.HeadObject(ctx, "audio/"+cand.ID+".caf")
		if herr != nil {
			return fmt.Errorf("HEAD failed (auth?): %w", herr)
		}
		if exists {
			t = cand
			found = true
			break
		}
	}
	fmt.Printf("sample track: %s\n  id=%s caf=%s (uploaded=%v)\n",
		t.DisplayTitleText, t.ID, t.CafPath, found)
	if !found {
		fmt.Println("auth OK (HEAD signed & accepted) but no probed CAF uploaded yet — re-run after sync.")
		return nil
	}
	key := "audio/" + t.ID + ".caf"
	url, err := client.PresignGet(key, 15*time.Minute)
	if err != nil {
		return err
	}
	fmt.Printf("presigned audio URL: %s\n", maskURL(url))

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch audio: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("audio fetch returned HTTP %d", resp.StatusCode)
	}
	fmt.Printf("audio reachable: HTTP %d, content-type=%s, content-length=%s\n",
		resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Range"))
	fmt.Println("OK — read-only consumer path verified end-to-end.")
	return nil
}

// maskURL hides the signature and credential query params so no key material is
// printed while still showing the object path.
func maskURL(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil {
		return "(unparseable)"
	}
	q := u.Query()
	for _, k := range []string{"X-Amz-Signature", "X-Amz-Credential"} {
		if q.Get(k) != "" {
			q.Set(k, "REDACTED")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func defaultCacheDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "parso-player")
	}
	return "./parso-player-cache"
}
