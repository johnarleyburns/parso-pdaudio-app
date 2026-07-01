// Command parso-pdaudio downloads public-domain classical recordings, converts
// them to Opus, packages iOS-ready CAF files, and indexes searchable metadata
// in a single SQLite database — driven by a Bubble Tea TUI with a built-in
// player.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/johnarleyburns/parso-pdaudio/internal/config"
	"github.com/johnarleyburns/parso-pdaudio/internal/db"
	"github.com/johnarleyburns/parso-pdaudio/internal/pipeline"
	"github.com/johnarleyburns/parso-pdaudio/internal/player"
	"github.com/johnarleyburns/parso-pdaudio/internal/provider"
	"github.com/johnarleyburns/parso-pdaudio/internal/sources"
	"github.com/johnarleyburns/parso-pdaudio/internal/tui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// Maintenance/data subcommands run outside the discovery pipeline.
	if len(args) > 0 {
		switch args[0] {
		case "compact":
			return runCompact(args[1:])
		case "enrich":
			return runEnrich(args[1:])
		case "dedup":
			return runDedup(args[1:])
		case "sync":
			return runSync(args[1:])
		case "r2check":
			return runR2Check(args[1:])
		}
	}

	cfg, err := config.Parse(args)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	store, err := db.Open(cfg.Dir)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	ctx, cancel := signalContext()
	defer cancel()

	// Resume safety: reset rows wedged in a transient status by a prior crash.
	if n, err := store.ResetTransient(ctx); err != nil {
		return fmt.Errorf("reset transient: %w", err)
	} else if n > 0 {
		fmt.Fprintf(os.Stderr, "resumed: reset %d in-flight rows\n", n)
	}

	keys, err := sources.Resolve(cfg.Sources)
	if err != nil {
		return err
	}
	client := provider.NewClient(cfg.UserAgent)
	provs, err := sources.Build(keys, client, &sources.BuildOpts{
		AllowAttribution: cfg.CommonsAllowAttribution,
	})
	if err != nil {
		return err
	}

	eng, err := pipeline.NewEngine(cfg, store, provs)
	if err != nil {
		return err
	}

	if cfg.ResetSkipped {
		if n, err := eng.ReviveSkipped(ctx); err != nil {
			return fmt.Errorf("revive skipped: %w", err)
		} else if n > 0 {
			fmt.Fprintf(os.Stderr, "reset-skipped: revived %d rows\n", n)
		}
	}
	if !eng.ConvertEnabled() {
		fmt.Fprintln(os.Stderr, "WARNING: ffmpeg/ffprobe not found on PATH — "+
			"convert/package/cleaner stages are disabled (downloads still run).")
	}

	pl := player.New()

	if cfg.PlayOnly {
		return runTUI(ctx, cfg, eng, store, pl, keys, true)
	}

	eng.StartWorkers(ctx)
	defer eng.Stop()

	if cfg.NoTUI {
		return runHeadless(ctx, eng, keys)
	}
	return runTUI(ctx, cfg, eng, store, pl, keys, false)
}

func runTUI(ctx context.Context, cfg *config.Config, eng *pipeline.Engine, store *db.DB, pl *player.Player, keys []string, playerOnly bool) error {
	m := tui.New(ctx, cfg, eng, store, pl, keys, playerOnly)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	pl.Stop()
	return err
}

func runHeadless(ctx context.Context, eng *pipeline.Engine, keys []string) error {
	// Drain progress messages so the channel never blocks workers.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-eng.Results:
			}
		}
	}()

	discDone := make(chan struct{})
	go func() {
		_ = eng.Discover(ctx)
		close(discDone)
	}()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	discovered := false

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "interrupted; state saved in DB (resume by re-running)")
			return nil
		case <-discDone:
			discovered = true
		case <-ticker.C:
		}
		printProgress(ctx, eng, keys)
		if discovered {
			done, err := pipelineComplete(ctx, eng)
			if err != nil {
				return err
			}
			if done {
				fmt.Println("done.")
				return nil
			}
		}
	}
}

func pipelineComplete(ctx context.Context, eng *pipeline.Engine) (bool, error) {
	pending, err := eng.Pending(ctx)
	if err != nil {
		return false, err
	}
	if eng.ConvertEnabled() {
		return pending == 0, nil
	}
	// ffmpeg missing: completion = nothing left to download.
	c, err := eng.Counts(ctx)
	if err != nil {
		return false, err
	}
	for _, m := range c {
		if m["discovered"] > 0 || m["downloading"] > 0 {
			return false, nil
		}
	}
	return true, nil
}

func printProgress(ctx context.Context, eng *pipeline.Engine, keys []string) {
	c, err := eng.Counts(ctx)
	if err != nil {
		return
	}
	var tDisc, tDone, tFail int
	for _, k := range keys {
		m := c[k]
		disc := sum(m) - m["skipped"]
		done := m["done"]
		fail := m["failed"]
		tDisc += disc
		tDone += done
		tFail += fail
		fmt.Printf("  %-16s disc=%-4d done=%-4d fail=%-3d skip=%-3d\n",
			k, disc, done, fail, m["skipped"])
	}
	fmt.Printf("TOTAL done=%d/%d fail=%d  workers D:%d C:%d P:%d X:%d\n\n",
		tDone, tDisc, tFail,
		eng.PoolSize(pipeline.StageDownload), eng.PoolSize(pipeline.StageConvert),
		eng.PoolSize(pipeline.StagePackage), eng.PoolSize(pipeline.StageCleaner))
}

func sum(m map[string]int) int {
	t := 0
	for _, v := range m {
		t += v
	}
	return t
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, cancel
}
