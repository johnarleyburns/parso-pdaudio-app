package provider

import (
	"encoding/json"
	"testing"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
)

func TestSelectBestPreference(t *testing.T) {
	rank := FormatRank([]string{"opus", "ogg", "wav", "mp3"})
	files := []core.CandidateFile{
		{URL: "a.mp3", Format: "mp3"},
		{URL: "a.ogg", Format: "ogg"},
		{URL: "a.wav", Format: "wav"},
	}
	best, ok := SelectBest(files, rank, false)
	if !ok || best.Format != "ogg" {
		t.Fatalf("want ogg, got %+v ok=%v", best, ok)
	}

	// No preferred format, no fallback -> skip.
	only := []core.CandidateFile{{URL: "a.aac", Format: "aac"}}
	if _, ok := SelectBest(only, rank, false); ok {
		t.Fatal("expected skip when no preferred format and no fallback")
	}
	// With fallback -> accept any audio.
	if f, ok := SelectBest(only, rank, true); !ok || f.Format != "aac" {
		t.Fatalf("fallback failed: %+v ok=%v", f, ok)
	}
}

func TestParseYear(t *testing.T) {
	cases := map[string]int{
		"1965":             1965,
		"recorded in 1842": 1842,
		"no year here":     0,
		"2024-05-01":       2024,
	}
	for in, want := range cases {
		if got := parseYear(in); got != want {
			t.Fatalf("parseYear(%q)=%d want %d", in, got, want)
		}
	}
}

func TestIAFormatToken(t *testing.T) {
	cases := map[string]string{
		"Ogg Vorbis":           "ogg",
		"VBR MP3":              "mp3",
		"Apple Lossless Audio": "other",
		"WAVE":                 "wav",
		"Opus":                 "opus",
		"PNG":                  "",
	}
	for in, want := range cases {
		if got := iaFormatToken(in); got != want {
			t.Fatalf("iaFormatToken(%q)=%q want %q", in, got, want)
		}
	}
}

func TestCommonsFormatToken(t *testing.T) {
	if commonsFormatToken("audio/ogg", "File:x.oga") != "ogg" {
		t.Fatal("oga should map to ogg")
	}
	if commonsFormatToken("audio/ogg", "File:x.opus") != "opus" {
		t.Fatal(".opus in audio/ogg should map to opus")
	}
	if commonsFormatToken("audio/x-wav", "File:x.wav") != "wav" {
		t.Fatal("wav mapping")
	}
	if commonsFormatToken("audio/mpeg", "File:x.mp3") != "mp3" {
		t.Fatal("mp3 mapping")
	}
	if commonsFormatToken("application/ogg", "File:x.ogg") != "ogg" {
		t.Fatal("application/ogg audio (.ogg) should map to ogg")
	}
	if commonsFormatToken("application/ogg", "File:x.opus") != "opus" {
		t.Fatal("application/ogg (.opus) should map to opus")
	}
	if commonsFormatToken("application/ogg", "File:x.ogv") != "" {
		t.Fatal("ogg video (.ogv) must be dropped")
	}
	if commonsFormatToken("video/webm", "File:x.webm") != "" {
		t.Fatal("non-audio must be dropped")
	}
}

func TestWorkKeyDedup(t *testing.T) {
	oga := `File:"America the Beautiful", performed by the United States Marine Band in the 1950s.oga`
	wav := `File:"America the Beautiful", performed by the United States Marine Band in the 1950s.wav`
	if workKeyFromTitle(oga) != workKeyFromTitle(wav) {
		t.Fatalf("oga/wav variants must dedup to same key:\n %q\n %q",
			workKeyFromTitle(oga), workKeyFromTitle(wav))
	}
	if workKeyFromTitle(oga) == "" {
		t.Fatal("empty work key")
	}
}

func TestParseClassicalTitle(t *testing.T) {
	composer, work, _ := parseClassicalTitle("Chopin, Ballade No. 1 - United States Marine Band")
	if composer != "Chopin" {
		t.Fatalf("composer = %q, want Chopin", composer)
	}
	if work == "" {
		t.Fatalf("work should be populated, got empty")
	}
}

func TestFlexStringUnmarshal(t *testing.T) {
	var s flexString
	if err := json.Unmarshal([]byte(`"hello"`), &s); err != nil || s != "hello" {
		t.Fatalf("string: %q err=%v", s, err)
	}
	if err := json.Unmarshal([]byte(`["a","b"]`), &s); err != nil || s != "a; b" {
		t.Fatalf("array: %q err=%v", s, err)
	}
	if err := json.Unmarshal([]byte(`null`), &s); err != nil || s != "" {
		t.Fatalf("null: %q err=%v", s, err)
	}
}
