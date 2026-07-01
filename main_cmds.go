package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/johnarleyburns/parso-pdaudio/internal/db"
	"github.com/johnarleyburns/parso-pdaudio/internal/dedup"
	"github.com/johnarleyburns/parso-pdaudio/internal/enrich"
	"github.com/johnarleyburns/parso-pdaudio/internal/r2"
)

// runCompact deletes the standalone .opus and .src.* files that are made
// redundant by the packaged .caf (the sole persisted audio format), reclaiming
// disk space, and nulls opus_path for done rows. It only deletes an opus/src
// file when a non-empty sibling .caf exists.
func runCompact(args []string) error {
	fs := flag.NewFlagSet("compact", flag.ContinueOnError)
	dir := fs.String("dir", "./library", "library directory (DB + media)")
	dry := fs.Bool("dry-run", false, "report what would be deleted without deleting")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store, err := db.Open(*dir)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()
	ctx := context.Background()

	cafExists := func(id string) bool {
		fi, err := os.Stat(filepath.Join(*dir, id+".caf"))
		return err == nil && fi.Size() > 0
	}

	var reclaimed int64
	var removed int
	// Sweep redundant opus + source files whose caf sibling is present.
	patterns := []string{"*.opus", "*.src.*"}
	for _, pat := range patterns {
		matches, _ := filepath.Glob(filepath.Join(*dir, pat))
		for _, m := range matches {
			base := filepath.Base(m)
			id := base
			if i := strings.IndexByte(base, '.'); i > 0 {
				id = base[:i]
			}
			if !cafExists(id) {
				continue // keep: no caf to fall back to
			}
			fi, statErr := os.Stat(m)
			if statErr != nil {
				continue
			}
			reclaimed += fi.Size()
			removed++
			if *dry {
				continue
			}
			if err := os.Remove(m); err != nil {
				fmt.Fprintf(os.Stderr, "warn: remove %s: %v\n", m, err)
			}
		}
	}

	// Null opus_path/opus_bytes on done rows now that opus files are gone.
	var nulled int64
	if !*dry {
		res, err := store.SQL().ExecContext(ctx,
			`UPDATE tracks SET opus_path=NULL, opus_bytes=NULL
			   WHERE status='done' AND caf_path IS NOT NULL AND opus_path IS NOT NULL`)
		if err != nil {
			return fmt.Errorf("null opus_path: %w", err)
		}
		nulled, _ = res.RowsAffected()
	}

	verb := "removed"
	if *dry {
		verb = "would remove"
	}
	fmt.Printf("compact: %s %d files, reclaimed %s; nulled opus_path on %d rows\n",
		verb, removed, humanBytes(reclaimed), nulled)
	return nil
}

// runEnrich runs the LLM enrichment + work-grouping pass.
func runEnrich(args []string) error {
	fs := flag.NewFlagSet("enrich", flag.ContinueOnError)
	dir := fs.String("dir", "./library", "library directory (DB + media)")
	limit := fs.Int("limit", 0, "max tracks to enrich this run (0 = all pending)")
	force := fs.Bool("force", false, "re-enrich already-enriched tracks")
	model := fs.String("model", enrich.DefaultModel, "bulk DeepSeek model id")
	conc := fs.Int("concurrency", 4, "parallel API calls")
	if err := fs.Parse(args); err != nil {
		return err
	}
	apiKey, err := enrich.LoadAPIKey()
	if err != nil {
		return err
	}
	store, err := db.Open(*dir)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	ctx := context.Background()
	return enrich.RunEnrich(ctx, store, enrich.Options{
		APIKey:      apiKey,
		Model:       *model,
		Limit:       *limit,
		Force:       *force,
		Concurrency: *conc,
		Progress: func(done, total int) {
			if done == total || done%25 == 0 {
				fmt.Printf("\renrich: %d/%d", done, total)
				if done == total {
					fmt.Println()
				}
			}
		},
	})
}

// runDedup fingerprints and marks duplicate tracks.
func runDedup(args []string) error {
	fs := flag.NewFlagSet("dedup", flag.ContinueOnError)
	dir := fs.String("dir", "./library", "library directory (DB + media)")
	force := fs.Bool("force", false, "re-fingerprint already-fingerprinted tracks")
	ber := fs.Float64("ber", 0.20, "max chromaprint bit-error-rate to treat as duplicate")
	durTol := fs.Float64("dur-tol", 3, "max duration difference (sec) for a duplicate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := db.Open(*dir)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()
	ctx := context.Background()
	return dedup.Run(ctx, store, dedup.Options{
		Dir: *dir, Force: *force, BER: *ber, DurTolSec: *durTol,
		Progress: func(done, total int) {
			if done == total || done%50 == 0 {
				fmt.Printf("\rdedup: fingerprinting %d/%d", done, total)
				if done == total {
					fmt.Println()
				}
			}
		},
	})
}

// runSync uploads canonical CAF files + the distribution DB to R2.
func runSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	dir := fs.String("dir", "./library", "library directory (DB + media)")
	dry := fs.Bool("dry-run", false, "list what would be uploaded without uploading")
	audioOnly := fs.Bool("audio-only", false, "upload only CAF files, not the DB")
	dbOnly := fs.Bool("db-only", false, "upload only the DB, not CAF files")
	forceDedup := fs.Bool("skip-dedup-check", false, "upload even if dedup has not run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// sync writes objects → use the read-write credential profile.
	cfg, err := r2.LoadConfigMode(r2.ModeReadWrite)
	if err != nil {
		return err
	}
	store, err := db.Open(*dir)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()
	ctx := context.Background()

	// Watermark: refuse to publish a DB that has not been deduped.
	if !*forceDedup {
		if n, err := store.CountUndeduped(ctx); err != nil {
			return err
		} else if n > 0 {
			return fmt.Errorf("%d done tracks not yet deduped — run `parso dedup` first (or --skip-dedup-check)", n)
		}
	}

	client := r2.New(cfg)

	// Audio: upload each canonical CAF.
	if !*dbOnly {
		tracks, err := store.ListCanonicalCAF(ctx)
		if err != nil {
			return err
		}
		var uploaded, skipped int
		var bytesUp int64
		for _, t := range tracks {
			if t.CafPath == "" {
				continue
			}
			key := "audio/" + t.ID + ".caf"
			local := filepath.Join(*dir, t.CafPath)
			fi, statErr := os.Stat(local)
			if statErr != nil {
				fmt.Fprintf(os.Stderr, "warn: missing %s\n", local)
				continue
			}
			if !*dry {
				exists, size, herr := client.HeadObject(ctx, key)
				if herr != nil {
					return herr
				}
				if exists && size == fi.Size() {
					skipped++
					continue
				}
				if err := client.PutFile(ctx, key, "audio/x-caf", local); err != nil {
					return err
				}
			}
			uploaded++
			bytesUp += fi.Size()
		}
		verb := "uploaded"
		if *dry {
			verb = "would upload"
		}
		fmt.Printf("sync: %s %d CAF files (%s), skipped %d already-present\n",
			verb, uploaded, humanBytes(bytesUp), skipped)
	}

	// DB: VACUUM INTO a snapshot and upload.
	if !*audioOnly {
		snapshot := filepath.Join(*dir, "library.dist.db")
		_ = os.Remove(snapshot)
		if _, err := store.SQL().ExecContext(ctx, `VACUUM INTO ?`, snapshot); err != nil {
			return fmt.Errorf("vacuum snapshot: %w", err)
		}
		defer os.Remove(snapshot)
		if *dry {
			fmt.Println("sync: would upload db/library.db")
		} else {
			if err := client.PutFile(ctx, "db/library.db", "application/x-sqlite3", snapshot); err != nil {
				return err
			}
			fmt.Println("sync: uploaded db/library.db")
		}
	}
	return nil
}

// runR2Check validates that the read-only and read-write credential profiles
// authenticate against R2, without printing any secret. It does a HEAD on a
// probe key (a 404 is success — it proves the request was signed and accepted).
func runR2Check(args []string) error {
	fs := flag.NewFlagSet("r2check", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	for _, mode := range []string{r2.ModeReadOnly, r2.ModeReadWrite} {
		cfg, err := r2.LoadConfigMode(mode)
		if err != nil {
			fmt.Printf("r2check %-4s: config error: %v\n", mode, err)
			continue
		}
		client := r2.New(cfg)
		exists, _, err := client.HeadObject(ctx, "healthcheck/__probe__")
		if err != nil {
			fmt.Printf("r2check %-4s: AUTH/REQUEST FAILED: %v\n", mode, err)
			continue
		}
		fmt.Printf("r2check %-4s: OK (bucket=%s endpoint=%s probe_exists=%v)\n",
			mode, cfg.Bucket, cfg.Host(), exists)
	}
	return nil
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(b)/float64(div), "KMGT"[exp])
}
