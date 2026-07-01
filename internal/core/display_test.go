package core

import "testing"

func TestDisplayTitleEnriched(t *testing.T) {
	tr := &Track{
		Source:            "commons_classical",
		Title:             "beethoven symphony 5 mvt 1.ogg",
		ComposerCanonical: "Ludwig van Beethoven",
		WorkTitle:         "Symphony No. 5 in C minor",
		Catalog:           "Op. 67",
		MovementTitle:     "I. Allegro con brio",
	}
	if got, want := DisplayTitle(tr, DisplayGlobal),
		"Ludwig van Beethoven — Symphony No. 5 in C minor, Op. 67 · I. Allegro con brio"; got != want {
		t.Errorf("global = %q, want %q", got, want)
	}
	if got, want := DisplayTitle(tr, DisplayComposer),
		"Symphony No. 5 in C minor, Op. 67 · I. Allegro con brio"; got != want {
		t.Errorf("composer = %q, want %q", got, want)
	}
	if got, want := DisplayTitle(tr, DisplayWork), "I. Allegro con brio"; got != want {
		t.Errorf("work = %q, want %q", got, want)
	}
}

func TestDisplayTitleCatalogNotDuplicated(t *testing.T) {
	tr := &Track{
		ComposerCanonical: "J.S. Bach",
		WorkTitle:         "Prelude No. 1 in C major, BWV 846",
		Catalog:           "BWV 846",
	}
	if got, want := DisplayTitle(tr, DisplayComposer), "Prelude No. 1 in C major, BWV 846"; got != want {
		t.Errorf("got %q, want %q (catalog should not be appended twice)", got, want)
	}
}

func TestDisplayTitleUnenrichedFallback(t *testing.T) {
	tr := &Track{Source: "navy", Title: "Anchors Aweigh"}
	if got, want := DisplayTitle(tr, DisplayGlobal), "[navy] Anchors Aweigh"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	tr2 := &Track{Source: "chopin", Title: "Ballade No. 1", Composer: "Chopin"}
	if got, want := DisplayTitle(tr2, DisplayGlobal), "Chopin — Ballade No. 1"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
