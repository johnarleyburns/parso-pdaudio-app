package db

import (
	"context"
	"testing"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v (FTS5 may be unavailable)", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func sampleTrack(id, src, url string) *core.Track {
	return &core.Track{
		ID:             id,
		Source:         src,
		SourceItem:     "musopen-chopin",
		Title:          "Ballade No. 1, Op. 23",
		Composer:       "Frédéric Chopin",
		OriginalURL:    url,
		OriginalFormat: "ogg",
		LicenseShort:   "CC0",
		Status:         core.StatusDiscovered,
	}
}

func TestSchemaAndFTS(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)

	tr := sampleTrack("01TESTID0000000000000000A", "chopin", "https://example/ballade1.ogg")
	ins, err := d.InsertCandidate(ctx, tr)
	if err != nil || !ins {
		t.Fatalf("insert: ins=%v err=%v", ins, err)
	}
	// Idempotent: second insert is a no-op.
	if ins2, _ := d.InsertCandidate(ctx, tr); ins2 {
		t.Fatalf("expected idempotent insert (no-op) on duplicate")
	}

	// Index for FTS + keyword search.
	if err := d.Index(ctx, tr.ID, []string{"chopin", "ballade", "op", "23"}, "chopin ballade op 23"); err != nil {
		t.Fatalf("index: %v", err)
	}
	got, err := d.Search(ctx, "chopin ballade", false, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 || got[0].ID != tr.ID {
		t.Fatalf("fts search returned %d rows, want the ballade", len(got))
	}
	// Stopword should not match.
	if none, _ := d.Search(ctx, "the", false, 10); len(none) != 0 {
		t.Fatalf("stopword 'the' should not match, got %d", len(none))
	}
}

func TestClaimPipelineFlow(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	tr := sampleTrack("01TESTID0000000000000000B", "chopin", "https://example/x.ogg")
	if _, err := d.InsertCandidate(ctx, tr); err != nil {
		t.Fatal(err)
	}

	// download
	c, ok, err := d.Claim(ctx, core.StatusDiscovered, core.StatusDownloading, "w1")
	if err != nil || !ok {
		t.Fatalf("claim discovered: ok=%v err=%v", ok, err)
	}
	if c.ID != tr.ID || c.OriginalURL != tr.OriginalURL {
		t.Fatalf("claim returned wrong row: %+v", c)
	}
	// double claim must find nothing
	if _, ok2, _ := d.Claim(ctx, core.StatusDiscovered, core.StatusDownloading, "w2"); ok2 {
		t.Fatalf("second claim should find no discovered rows")
	}
	if err := d.SetDownloaded(ctx, c.ID, 12345, "deadbeef"); err != nil {
		t.Fatal(err)
	}

	// convert
	cc, ok, _ := d.Claim(ctx, core.StatusDownloaded, core.StatusConverting, "w1")
	if !ok {
		t.Fatal("claim downloaded failed")
	}
	if err := d.SetConverted(ctx, cc.ID, cc.ID+".opus", 999, 73.2, "vorbis"); err != nil {
		t.Fatal(err)
	}
	// package
	pc, ok, _ := d.Claim(ctx, core.StatusConverted, core.StatusPackaging, "w1")
	if !ok {
		t.Fatal("claim converted failed")
	}
	if err := d.SetPackaged(ctx, pc.ID, pc.ID+".caf", 1001); err != nil {
		t.Fatal(err)
	}
	// clean
	kc, ok, _ := d.Claim(ctx, core.StatusPackaged, core.StatusCleaning, "w1")
	if !ok {
		t.Fatal("claim packaged failed")
	}
	if err := d.SetDone(ctx, kc.ID); err != nil {
		t.Fatal(err)
	}

	full, err := d.GetTrack(ctx, tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if full.Status != core.StatusDone {
		t.Fatalf("status = %s, want done", full.Status)
	}
	// Provenance survives.
	if full.OriginalURL == "" || full.OriginalSHA1 != "deadbeef" || full.OriginalFormat != "ogg" {
		t.Fatalf("provenance lost: %+v", full)
	}
	if full.OpusBytes != 999 || full.CafBytes != 1001 || full.DurationSec == 0 {
		t.Fatalf("artifact fields wrong: %+v", full)
	}

	playable, err := d.ListPlayable(ctx, 10)
	if err != nil || len(playable) != 1 {
		t.Fatalf("ListPlayable = %d err=%v, want 1", len(playable), err)
	}
}

func TestResetTransientAndRetry(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	tr := sampleTrack("01TESTID0000000000000000C", "marine", "https://example/y.ogg")
	if _, err := d.InsertCandidate(ctx, tr); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-download.
	if _, _, err := d.Claim(ctx, core.StatusDiscovered, core.StatusDownloading, "w1"); err != nil {
		t.Fatal(err)
	}
	n, err := d.ResetTransient(ctx)
	if err != nil || n != 1 {
		t.Fatalf("reset transient = %d err=%v, want 1", n, err)
	}
	got, _ := d.GetTrack(ctx, tr.ID)
	if got.Status != core.StatusDiscovered {
		t.Fatalf("after reset status = %s, want discovered", got.Status)
	}

	// Fail then retry.
	if _, _, err := d.Claim(ctx, core.StatusDiscovered, core.StatusDownloading, "w1"); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkFailed(ctx, tr.ID, "boom"); err != nil {
		t.Fatal(err)
	}
	reset, err := d.RetryFailed(ctx, 3)
	if err != nil || reset != 1 {
		t.Fatalf("retry = %d err=%v, want 1", reset, err)
	}
	got, _ = d.GetTrack(ctx, tr.ID)
	if got.Status != core.StatusDiscovered {
		t.Fatalf("after retry status = %s, want discovered", got.Status)
	}
}

func TestCounts(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t)
	for i, st := range []string{"a", "b", "c"} {
		tr := sampleTrack("01TESTID000000000000000"+st+itoaTest(i), "chopin", "https://example/"+st)
		if _, err := d.InsertCandidate(ctx, tr); err != nil {
			t.Fatal(err)
		}
	}
	c, err := d.Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c["chopin"][core.StatusDiscovered] != 3 {
		t.Fatalf("counts = %+v, want 3 discovered", c)
	}
}

func itoaTest(i int) string { return string(rune('A' + i)) }
