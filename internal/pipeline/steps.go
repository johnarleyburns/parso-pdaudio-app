package pipeline

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
	"github.com/johnarleyburns/parso-pdaudio/internal/meta"
)

func formatExt(format string) string {
	switch format {
	case "ogg", "opus", "wav", "mp3", "flac":
		return format
	case "oga":
		return "oga"
	default:
		return "dat"
	}
}

func (e *Engine) srcPath(id, format string) string {
	return filepath.Join(e.cfg.Dir, id+".src."+formatExt(format))
}
func (e *Engine) opusPath(id string) string { return filepath.Join(e.cfg.Dir, id+".opus") }
func (e *Engine) cafPath(id string) string  { return filepath.Join(e.cfg.Dir, id+".caf") }

func (e *Engine) stepDownload(ctx context.Context, wid string) bool {
	t, ok, err := e.store.Claim(ctx, core.StatusDiscovered, core.StatusDownloading, wid)
	if err != nil || !ok {
		return false
	}
	e.WorkerBegin(wid, StageDownload, t.Source, displayTitleForWorker(t))
	defer e.WorkerEnd(wid)

	dst := e.srcPath(t.ID, t.OriginalFormat)
	bytes, sha, requeue, rejErr := e.downloadWithLimiter(ctx, t, dst)
	if requeue {
		e.requeueTrack(ctx, t, 30)
		e.WorkerBackoff(wid, "429 requeue 30s")
		return true
	}
	if rejErr != nil {
		_ = os.Remove(dst)
		return e.failTrack(ctx, t, StageDownload, rejErr)
	}
	if err := e.store.SetDownloaded(ctx, t.ID, bytes, sha); err != nil {
		return e.failTrack(ctx, t, StageDownload, err)
	}
	e.emit(core.ProgressMsg{Source: t.Source, Stage: StageDownload, TrackID: t.ID, Completed: true})
	e.Logf("OK download %s %s (%s)", t.Source, t.Title, humanSize(bytes))
	return true
}

func (e *Engine) downloadWithLimiter(ctx context.Context, t *core.Track, dst string) (bytes int64, sha string, requeue bool, err error) {
	host := hostFromURL(t.OriginalURL)
	for attempt := 0; attempt < e.cfg.MaxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return 0, "", false, ctx.Err()
			case <-time.After(time.Duration(1<<uint(attempt-1)) * time.Second):
			}
		}
		if e.rlim != nil && host != "" {
			if werr := e.rlim.Wait(ctx, host); werr != nil {
				return 0, "", false, werr
			}
		}
		n, sha1sum, retryAfter, dErr := e.downloadOnce(ctx, t, dst)
		if dErr == nil {
			e.WorkerUpdate("", n)
			return n, sha1sum, false, nil
		}
		if retryAfter > 0 {
			select {
			case <-ctx.Done():
				return 0, "", false, ctx.Err()
			case <-time.After(time.Duration(retryAfter) * time.Second):
			}
			continue
		}
		if dErr != nil && isRateLimitErr(dErr) {
			if attempt >= e.cfg.MaxAttempts-1 {
				return 0, "", true, dErr
			}
			continue
		}
		if dErr != nil && isRetryableErr(dErr) {
			continue
		}
		return 0, "", false, dErr
	}
	return 0, "", true, fmt.Errorf("exhausted download retries")
}

func (e *Engine) downloadOnce(ctx context.Context, t *core.Track, dst string) (int64, string, int, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, t.OriginalURL, nil)
	if err != nil {
		return 0, "", 0, err
	}
	req.Header.Set("User-Agent", e.cfg.UserAgent)

	resp, err := e.httpDL.Do(req)
	if err != nil {
		return 0, "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return 0, "", 0, fmt.Errorf("http %d", resp.StatusCode)
	}
	if resp.StatusCode == 429 {
		ra := 0
		if v := resp.Header.Get("Retry-After"); v != "" {
			if s, perr := strconv.Atoi(v); perr == nil && s > 0 {
				ra = s
			}
		}
		if ra == 0 {
			ra = 5
		}
		return 0, "", ra, fmt.Errorf("http 429")
	}
	if resp.StatusCode >= 500 {
		return 0, "", 0, fmt.Errorf("http %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, "", 0, fmt.Errorf("http %d", resp.StatusCode)
	}

	f, err := os.Create(dst)
	if err != nil {
		return 0, "", 0, err
	}
	h := sha1.New()
	buf := make([]byte, 64*1024)
	var total int64
	for {
		nr, rerr := resp.Body.Read(buf)
		if nr > 0 {
			if _, werr := f.Write(buf[:nr]); werr != nil {
				_ = f.Close()
				return 0, "", 0, werr
			}
			h.Write(buf[:nr])
			total += int64(nr)
			e.WorkerUpdate("", total)
			e.emit(core.ProgressMsg{Source: t.Source, Stage: StageDownload, TrackID: t.ID, BytesDelta: int64(nr)})
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = f.Close()
			return 0, "", 0, rerr
		}
	}
	if err := f.Close(); err != nil {
		return 0, "", 0, err
	}
	if total == 0 {
		return 0, "", 0, fmt.Errorf("empty download")
	}
	return total, hex.EncodeToString(h.Sum(nil)), 0, nil
}

func hostFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

func isRateLimitErr(err error) bool {
	return err != nil && (err.Error() == "http 429" || err.Error() == "http 403")
}

func isRetryableErr(err error) bool {
	s := err.Error()
	return s == "http 429" || s == "http 403" || len(s) > 5 && s[:5] == "http 5"
}

func humanSize(b int64) string {
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

func displayTitleForWorker(t *core.Track) string {
	if t.Title != "" {
		return t.Title
	}
	if t.Work != "" {
		return t.Work
	}
	return t.SourceItem
}

func (e *Engine) stepConvert(ctx context.Context, wid string) bool {
	t, ok, err := e.store.Claim(ctx, core.StatusDownloaded, core.StatusConverting, wid)
	if err != nil || !ok {
		return false
	}
	e.WorkerBegin(wid, StageConvert, t.Source, displayTitleForWorker(t))
	defer e.WorkerEnd(wid)

	src := e.srcPath(t.ID, t.OriginalFormat)
	codec, dur, err := e.tools.Probe(ctx, src)
	if err != nil {
		return e.failTrack(ctx, t, StageConvert, err)
	}
	// B.3.2 Fragment floor — authoritative convert gate
	if e.cfg.MinDurationSec > 0 && dur < e.cfg.MinDurationSec {
		_ = e.store.MarkSkipped(ctx, t.ID, fmt.Sprintf("fragment: duration %.1fs < %.0fs", dur, e.cfg.MinDurationSec))
		e.emit(core.ProgressMsg{Source: t.Source, Stage: StageConvert, TrackID: t.ID, Skipped: true})
		e.Logf("SKIP %s %s: fragment (%.1fs < %.0fs)", t.Source, displayTitleForWorker(t), dur, e.cfg.MinDurationSec)
		return true
	}
	out := e.opusPath(t.ID)
	if err := e.tools.ToOpus(ctx, src, out, codec, e.cfg.OpusBitrate); err != nil {
		return e.failTrack(ctx, t, StageConvert, err)
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Size() == 0 {
		return e.failTrack(ctx, t, StageConvert, fmt.Errorf("opus output missing/empty"))
	}
	rel := filepath.Base(out)
	if err := e.store.SetConverted(ctx, t.ID, rel, fi.Size(), dur, codec); err != nil {
		return e.failTrack(ctx, t, StageConvert, err)
	}
	e.indexTrack(ctx, t.ID)
	e.emit(core.ProgressMsg{Source: t.Source, Stage: StageConvert, TrackID: t.ID, Completed: true})
	e.Logf("OK convert %s %s (%.1fs %s)", t.Source, t.Title, dur, codec)
	return true
}

func (e *Engine) indexTrack(ctx context.Context, id string) {
	full, err := e.store.GetTrack(ctx, id)
	if err != nil {
		return
	}
	kws, blob := meta.Build(full)
	_ = e.store.Index(ctx, id, kws, blob)
}

func (e *Engine) stepPackage(ctx context.Context, wid string) bool {
	t, ok, err := e.store.Claim(ctx, core.StatusConverted, core.StatusPackaging, wid)
	if err != nil || !ok {
		return false
	}
	e.WorkerBegin(wid, StagePackage, t.Source, displayTitleForWorker(t))
	defer e.WorkerEnd(wid)

	opus := e.opusPath(t.ID)
	caf := e.cafPath(t.ID)
	if err := e.pkg.Package(ctx, opus, caf); err != nil {
		return e.failTrack(ctx, t, StagePackage, err)
	}
	fi, err := os.Stat(caf)
	if err != nil || fi.Size() == 0 {
		return e.failTrack(ctx, t, StagePackage, fmt.Errorf("caf output missing/empty"))
	}
	if err := e.store.SetPackaged(ctx, t.ID, filepath.Base(caf), fi.Size()); err != nil {
		return e.failTrack(ctx, t, StagePackage, err)
	}
	e.emit(core.ProgressMsg{Source: t.Source, Stage: StagePackage, TrackID: t.ID, Completed: true})
	e.Logf("OK package %s %s", t.Source, t.Title)
	return true
}

func (e *Engine) stepCleaner(ctx context.Context, wid string) bool {
	t, ok, err := e.store.Claim(ctx, core.StatusPackaged, core.StatusCleaning, wid)
	if err != nil || !ok {
		return false
	}
	e.WorkerBegin(wid, StageCleaner, t.Source, displayTitleForWorker(t))
	defer e.WorkerEnd(wid)

	opus := e.opusPath(t.ID)
	caf := e.cafPath(t.ID)
	if !nonEmpty(opus) || !nonEmpty(caf) {
		return e.failTrack(ctx, t, StageCleaner, fmt.Errorf("missing opus/caf before cleanup"))
	}
	if !e.cfg.KeepSource {
		_ = os.Remove(e.srcPath(t.ID, t.OriginalFormat))
	}
	if err := e.store.SetDone(ctx, t.ID); err != nil {
		return e.failTrack(ctx, t, StageCleaner, err)
	}
	e.emit(core.ProgressMsg{Source: t.Source, Stage: StageCleaner, TrackID: t.ID, Completed: true})
	e.Logf("OK done %s %s", t.Source, t.Title)
	return true
}

func nonEmpty(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Size() > 0
}
