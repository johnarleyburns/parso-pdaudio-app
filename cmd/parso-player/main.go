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
	"os"
	"path/filepath"

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

	pl := player.New()
	audioCache := filepath.Join(*cacheDir, "audio")
	_ = os.MkdirAll(audioCache, 0o755)

	m := newModel(store, pl, client, audioCache)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	pl.Stop()
	return err
}

func defaultCacheDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "parso-player")
	}
	return "./parso-player-cache"
}
