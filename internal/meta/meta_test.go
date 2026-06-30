package meta

import (
	"testing"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
)

func TestTokenizeStopwordsAndDiacritics(t *testing.T) {
	got := Tokenize("The Pathétique Sonata and the Op. 13")
	set := map[string]bool{}
	for _, w := range got {
		set[w] = true
	}
	if set["the"] || set["and"] {
		t.Fatalf("stopwords leaked: %v", got)
	}
	if !set["pathetique"] {
		t.Fatalf("diacritic fold failed, want 'pathetique': %v", got)
	}
	if !set["sonata"] || !set["op"] {
		t.Fatalf("classical words dropped: %v", got)
	}
	// length-1 tokens dropped
	for _, w := range got {
		if len([]rune(w)) < 2 {
			t.Fatalf("length-1 token kept: %q", w)
		}
	}
}

func TestBuildDedupAndSort(t *testing.T) {
	tr := &core.Track{
		Title:    "Ballade No. 1",
		Composer: "Chopin",
		Work:     "Ballade No. 1 in G minor",
		Source:   "chopin",
	}
	kws, blob := Build(tr)
	if len(kws) == 0 || blob == "" {
		t.Fatalf("empty build")
	}
	seen := map[string]bool{}
	prev := ""
	for _, k := range kws {
		if seen[k] {
			t.Fatalf("duplicate keyword %q", k)
		}
		if prev != "" && k < prev {
			t.Fatalf("keywords not sorted: %q before %q", prev, k)
		}
		seen[k] = true
		prev = k
	}
	if !seen["chopin"] || !seen["ballade"] || !seen["minor"] {
		t.Fatalf("expected keywords missing: %v", kws)
	}
}
