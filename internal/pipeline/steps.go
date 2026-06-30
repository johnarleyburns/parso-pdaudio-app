package pipeline

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

// stepDownload claims a discovered row and downloads the native file.
func (e *Engine) stepDownload(ctx context.Context, wid string) bool {
	t, ok, err := e.store.Claim(ctx, core.StatusDiscovered, core.StatusDownloading, wid)
	if err != nil || !ok {
		return false
	}
	dst := e.srcPath(t.ID, t.OriginalFormat)
	bytes, sha, err := e.download(ctx, t.Source, t.ID, t.OriginalURL, dst)
	if err != nil {
		_ = os.Remove(dst)
		return e.failTrack(ctx, t, StageDownload, err)
	}
	if err := e.store.SetDownloaded(ctx, t.ID, bytes, sha); err != nil {
		return e.failTrack(ctx, t, StageDownload, err)
	}
	e.emit(core.ProgressMsg{Source: t.Source, Stage: StageDownload, TrackID: t.ID, Completed: true})
	return true
}

func (e *Engine) download(ctx context.Context, source, id, url, dst string) (int64, string, error) {
	var lastErr error
	for attempt := 0; attempt < e.cfg.MaxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return 0, "", ctx.Err()
			case <-time.After(time.Duration(1<<uint(attempt-1)) * time.Second):
			}
		}
		n, sha, retry, err := e.downloadOnce(ctx, source, id, url, dst)
		if err == nil {
			return n, sha, nil
		}
		lastErr = err
		if !retry {
			return 0, "", err
		}
	}
	return 0, "", lastErr
}

func (e *Engine) downloadOnce(ctx context.Context, source, id, url, dst string) (int64, string, bool, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", false, err
	}
	req.Header.Set("User-Agent", e.cfg.UserAgent)

	resp, err := e.httpDL.Do(req)
	if err != nil {
		return 0, "", true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 0, "", false, fmt.Errorf("http 404")
	}
	if resp.StatusCode != http.StatusOK {
		retry := resp.StatusCode == 429 || resp.StatusCode >= 500
		return 0, "", retry, fmt.Errorf("http %d", resp.StatusCode)
	}

	f, err := os.Create(dst)
	if err != nil {
		return 0, "", false, err
	}
	h := sha1.New()
	buf := make([]byte, 64*1024)
	var total int64
	for {
		nr, rerr := resp.Body.Read(buf)
		if nr > 0 {
			if _, werr := f.Write(buf[:nr]); werr != nil {
				_ = f.Close()
				return 0, "", false, werr
			}
			h.Write(buf[:nr])
			total += int64(nr)
			e.emit(core.ProgressMsg{Source: source, Stage: StageDownload, TrackID: id, BytesDelta: int64(nr)})
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = f.Close()
			return 0, "", true, rerr
		}
	}
	if err := f.Close(); err != nil {
		return 0, "", false, err
	}
	if total == 0 {
		return 0, "", true, fmt.Errorf("empty download")
	}
	return total, hex.EncodeToString(h.Sum(nil)), false, nil
}

// stepConvert claims a downloaded row and converts the native file to .opus.
func (e *Engine) stepConvert(ctx context.Context, wid string) bool {
	t, ok, err := e.store.Claim(ctx, core.StatusDownloaded, core.StatusConverting, wid)
	if err != nil || !ok {
		return false
	}
	src := e.srcPath(t.ID, t.OriginalFormat)
	codec, dur, err := e.tools.Probe(ctx, src)
	if err != nil {
		return e.failTrack(ctx, t, StageConvert, err)
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

// stepPackage claims a converted row and packages the .opus into a .caf.
func (e *Engine) stepPackage(ctx context.Context, wid string) bool {
	t, ok, err := e.store.Claim(ctx, core.StatusConverted, core.StatusPackaging, wid)
	if err != nil || !ok {
		return false
	}
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
	return true
}

// stepCleaner verifies artifacts, deletes the native file, marks done.
func (e *Engine) stepCleaner(ctx context.Context, wid string) bool {
	t, ok, err := e.store.Claim(ctx, core.StatusPackaged, core.StatusCleaning, wid)
	if err != nil || !ok {
		return false
	}
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
	return true
}

func nonEmpty(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Size() > 0
}
