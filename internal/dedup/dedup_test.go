package dedup

import (
	"testing"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
)

func TestBERMatchIdentical(t *testing.T) {
	fp := []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21}
	if !berMatch(fp, fp, 0.20) {
		t.Fatal("identical fingerprints should match")
	}
}

func TestBERMatchDifferent(t *testing.T) {
	a := make([]uint32, 40)
	b := make([]uint32, 40)
	for i := range a {
		a[i] = 0x00000000
		b[i] = 0xFFFFFFFF // every bit differs
	}
	if berMatch(a, b, 0.20) {
		t.Fatal("fully-different fingerprints should not match")
	}
}

func TestPickCanonicalPrefersLicenseThenSource(t *testing.T) {
	cl := []*core.Track{
		{ID: "b", Source: "commons_classical", LicenseShort: "CC-BY", CafBytes: 100},
		{ID: "a", Source: "commons_classical", LicenseShort: "CC0", CafBytes: 50},
	}
	if got := pickCanonical(cl); got.ID != "a" {
		t.Errorf("expected CC0 track to be canonical, got %s", got.ID)
	}

	cl2 := []*core.Track{
		{ID: "y", Source: "navy", LicenseShort: "PD", CafBytes: 100},
		{ID: "x", Source: "bach_wtc1", LicenseShort: "PD", CafBytes: 100},
	}
	if got := pickCanonical(cl2); got.ID != "x" {
		t.Errorf("expected higher-trust source to win, got %s", got.ID)
	}
}
