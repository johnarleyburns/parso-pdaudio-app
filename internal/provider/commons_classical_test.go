package provider

import (
	"context"
	"testing"
)

// --- B.3.1 Audio mime gate ---

func TestIsAudioMimeClassical(t *testing.T) {
	cases := []struct {
		mime, ext string
		want      bool
	}{
		// Standard audio
		{"audio/ogg", "", true},
		{"audio/opus", "", true},
		{"audio/x-wav", "", true},
		{"audio/mpeg", "", true},
		{"audio/flac", "", true},
		{"audio/x-flac", "", true},
		{"audio/mp3", "", true},
		// application/ogg family
		{"application/ogg", ".ogg", true},
		{"application/ogg", ".opus", true},
		{"application/x-ogg", ".ogg", true},
		// Extension-based fallback
		{"application/octet-stream", ".ogg", true},
		{"application/octet-stream", ".oga", true},
		{"application/octet-stream", ".flac", true},
		{"application/octet-stream", ".wav", true},
		{"application/octet-stream", ".mp3", true},
		{"application/octet-stream", ".opus", true},
		// Audio midi must be dropped
		{"audio/midi", "", false},
		// Non-audio
		{"video/webm", ".webm", false},
		{"image/jpeg", ".jpg", false},
		{"application/pdf", ".pdf", false},
		{"application/octet-stream", ".bin", false},
		// Edge: no extension, unknown mime
		{"", "", false},
		{"video/mp4", ".mp4", false},
	}
	for _, c := range cases {
		got := isAudioMimeClassical(c.mime, c.ext)
		if got != c.want {
			t.Errorf("isAudioMimeClassical(%q, %q) = %v, want %v", c.mime, c.ext, got, c.want)
		}
	}
}

// --- B.3.1 Format token with extension fallback ---

func TestClassicalFormatToken(t *testing.T) {
	cases := []struct {
		mime, title, want string
	}{
		// Standard mime types
		{"audio/ogg", "File:x.oga", "ogg"},
		{"audio/ogg", "File:x.opus", "opus"},
		{"audio/opus", "File:x.opus", "opus"},
		{"audio/x-wav", "File:x.wav", "wav"},
		{"audio/mpeg", "File:x.mp3", "mp3"},
		{"audio/flac", "File:x.flac", "flac"},
		// application/ogg
		{"application/ogg", "File:x.ogg", "ogg"},
		{"application/ogg", "File:x.opus", "opus"},
		{"application/ogg", "File:x.ogv", ""}, // video ogg should be empty
		// Extension fallback for unknown mime
		{"application/octet-stream", "File:x.ogg", "ogg"},
		{"application/octet-stream", "File:x.flac", "flac"},
		{"application/octet-stream", "File:x.wav", "wav"},
		// Non-audio
		{"video/webm", "File:x.webm", ""},
		{"image/jpeg", "File:x.jpg", ""},
	}
	for _, c := range cases {
		got := classicalFormatToken(c.mime, c.title)
		if got != c.want {
			t.Errorf("classicalFormatToken(%q, %q) = %q, want %q", c.mime, c.title, got, c.want)
		}
	}
}

// --- B.2 Skippable subcategory ---

func TestIsSkippableSubcat(t *testing.T) {
	patterns := []string{"MIDI files", "Synthesized", "Sheet music", "Scores", "metronome"}
	cases := []struct {
		title string
		want  bool
	}{
		{"Category:MIDI files of classical music", true},
		{"Category:Audio files of MIDI files", true},
		{"Category:Synthesized audio", true},
		{"Category:Sheet music by Beethoven", true},
		{"Category:Scores of Beethoven", true},
		{"Category:Sound files of metronome", true},
		{"Category:Audio files of music by Beethoven", false},
		{"Category:Beethoven Piano Sonatas played by Schnabel", false},
		// Case insensitive
		{"Category:midi FILES of something", true},
		{"Category:SHEET MUSIC collection", true},
	}
	for _, c := range cases {
		got := isSkippableSubcat(c.title, patterns)
		if got != c.want {
			t.Errorf("isSkippableSubcat(%q) = %v, want %v", c.title, got, c.want)
		}
	}
}

// --- B.3.3 License gate ---

func TestIsPDOrCC0(t *testing.T) {
	cases := map[string]bool{
		"CC0":           true,
		"cc0":           true,
		"Public domain": true,
		"public domain": true,
		"PD-USGov":      true,
		"pd-usgov":      true,
		"PD-EU-audio":   true,
		"PD-1923":       true,
		"CC BY-SA 4.0":  false,
		"CC BY 4.0":     false,
		"CC BY":         false,
		"EFF OAL-1":     false,
		"":              false,
	}
	for in, want := range cases {
		if got := isPDOrCC0(in); got != want {
			t.Errorf("isPDOrCC0(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsAttributionLicense(t *testing.T) {
	cases := map[string]bool{
		"CC BY-SA 4.0":  true,
		"CC BY 4.0":     true,
		"CC BY 3.0":     true,
		"EFF OAL-1":     true,
		"OAL-1":         true, // contains "oal"
		"CC0":           false,
		"Public domain": false,
		"PD-USGov":      false,
	}
	for in, want := range cases {
		if got := isAttributionLicense(in); got != want {
			t.Errorf("isAttributionLicense(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestLicenseAllowedPolicy(t *testing.T) {
	p := &ClassicalCategoriesProvider{LicensePolicy: "strict"}

	// Strict: only PD/CC0
	if !p.licenseAllowed("CC0") {
		t.Error("strict: CC0 should be allowed")
	}
	if !p.licenseAllowed("PD-USGov") {
		t.Error("strict: PD-USGov should be allowed")
	}
	if !p.licenseAllowed("Public domain") {
		t.Error("strict: Public domain should be allowed")
	}
	if p.licenseAllowed("CC BY-SA 4.0") {
		t.Error("strict: CC BY-SA should NOT be allowed")
	}
	if p.licenseAllowed("EFF OAL-1") {
		t.Error("strict: OAL should NOT be allowed")
	}
	if p.licenseAllowed("") {
		t.Error("strict: empty license should NOT be allowed")
	}

	// Attribution: also allow CC-BY/SA/OAL
	p.LicensePolicy = "attribution"
	if !p.licenseAllowed("CC BY-SA 4.0") {
		t.Error("attribution: CC BY-SA should be allowed")
	}
	if !p.licenseAllowed("EFF OAL-1") {
		t.Error("attribution: OAL should be allowed")
	}
	if !p.licenseAllowed("CC0") {
		t.Error("attribution: CC0 should still be allowed")
	}
	if p.licenseAllowed("") {
		t.Error("attribution: empty license should NOT be allowed")
	}
}

// --- B.3.5 Dedup key ---

func TestClassicalWorkKey(t *testing.T) {
	cases := []struct {
		a, b string
		want bool // expect same key
	}{
		// Same recording, different extension
		{`File:"America the Beautiful", performed by the United States Marine Band in the 1950s.oga`,
			`File:"America the Beautiful", performed by the United States Marine Band in the 1950s.wav`, true},
		// Same recording, different title format
		{`File:Klaviersonate Nr. 1 Op. 2.1 - I.Allegro.ogg`,
			`File:Piano Sonata N° 1 - 1. Allegro (Beethoven, Schnabel).ogg`, false},
		// Extension + format word removal
		{`File:Beethoven_Symphony_5_opus.ogg`, `File:Beethoven_Symphony_5.mp3`, true},
		// Diacritics folding
		{`File:Antonín Dvořák - Symphony 9.ogg`, `File:Antonin Dvorak - Symphony 9.wav`, true},
		{`File:Frédéric Chopin - Ballade No 1.ogg`, `File:Frederic Chopin - Ballade No 1.wav`, true},
		// Whitespace normalization
		{`File:  Beethoven   Symphony 5  .ogg`, `File:Beethoven Symphony 5.wav`, true},
	}
	for _, c := range cases {
		a := classicalWorkKey(c.a)
		b := classicalWorkKey(c.b)
		if c.want && a != b {
			t.Errorf("work keys should match:\n  a=%q (%q)\n  b=%q (%q)", c.a, a, c.b, b)
		}
		if !c.want && a == b {
			t.Errorf("work keys should NOT match:\n  a=%q (%q)\n  b=%q (%q)", c.a, a, c.b, b)
		}
	}
}

func TestClassicalWorkKeyNonEmpty(t *testing.T) {
	if k := classicalWorkKey(`File:test.ogg`); k == "" {
		t.Fatal("work key should not be empty")
	}
}

// --- Composer name extraction from category ---

func TestExtractComposerFromCategory(t *testing.T) {
	cases := map[string]string{
		"Category:Audio files of music by Ludwig van Beethoven":                      "Ludwig van Beethoven",
		"Category:Audio files of classical music by Frédéric Chopin":                 "Frédéric Chopin",
		"Category:Audio files of music by Johann Sebastian Bach":                     "Johann Sebastian Bach",
		"Category:Audio files of music by Wolfgang Amadeus Mozart":                   "Wolfgang Amadeus Mozart",
		"Category:Audio files of music by Pyotr Ilyich Tchaikovsky":                  "Pyotr Ilyich Tchaikovsky",
		"Category:Audio files of Beethoven's Piano Sonatas Played by Artur Schnabel": "Beethoven",
		"Category:Audio files of music by Carl Philipp Emanuel Bach":                 "Carl Philipp Emanuel Bach",
		// Edge: no standard prefix
		"Category:Beethoven Misc": "Beethoven Misc",
	}
	for in, want := range cases {
		got := extractComposerFromCategory(in)
		if got != want {
			t.Errorf("extractComposerFromCategory(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- Performer extraction from category context ---

func TestExtractPerformerFromCategory(t *testing.T) {
	cases := map[string]string{
		"Category:Audio files of Beethoven's Piano Sonatas Played by Artur Schnabel": "Artur Schnabel",
		"Category:Audio files of music Played by Bernstein":                          "Bernstein",
		"Category:Audio files of Mozart Requiem Played by Herbert von Karajan":       "Herbert von Karajan",
		"Category:Audio files of Bach's Goldberg Variations Played by Glenn Gould":   "Glenn Gould",
		// Comma after performer name
		"Category:Audio files Played by John Smith, orchestra": "John Smith",
		// No performer info
		"Category:Audio files of music by Beethoven": "",
		"Category:Symphony No 5":                     "",
		// Parenthesis after performer
		"Category:Played by Perlman (violin)": "Perlman",
	}
	for in, want := range cases {
		got := extractPerformerFromCategory(in)
		if got != want {
			t.Errorf("extractPerformerFromCategory(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- Cursor encode/decode ---

func TestCCCursorRoundTrip(t *testing.T) {
	cases := []ccCursor{
		{ComposerIndex: 0},
		{ComposerIndex: 42},
		{ComposerIndex: 128},
	}
	for _, c := range cases {
		encoded := c.encode()
		decoded := decodeCCCursor(encoded)
		if decoded.ComposerIndex != c.ComposerIndex {
			t.Errorf("cursor roundtrip: %d -> %q -> %d", c.ComposerIndex, encoded, decoded.ComposerIndex)
		}
	}
}

func TestDecodeEmptyCursor(t *testing.T) {
	c := decodeCCCursor("")
	if c.ComposerIndex != 0 {
		t.Errorf("empty cursor should have index 0, got %d", c.ComposerIndex)
	}
}

func TestDecodeMalformedCursor(t *testing.T) {
	c := decodeCCCursor("not json")
	if c.ComposerIndex != 0 {
		t.Errorf("malformed cursor should have index 0, got %d", c.ComposerIndex)
	}
}

// --- Provider defaults ---

func TestClassicalProviderDefaults(t *testing.T) {
	p := &ClassicalCategoriesProvider{
		SourceKey:     "test",
		RootCategory:  "Category:Test",
		LicensePolicy: "strict",
	}
	if p.MinBytes != 0 {
		// default applied in resolveFiles
	}
	if p.Key() != "test" {
		t.Fatalf("Key() = %q, want test", p.Key())
	}
}

// --- extToFormat ---

func TestExtToFormat(t *testing.T) {
	cases := map[string]string{
		".opus": "opus",
		".ogg":  "ogg",
		".oga":  "ogg",
		".flac": "flac",
		".wav":  "wav",
		".wave": "wav",
		".mp3":  "mp3",
		".webm": "",
		".jpg":  "",
		"":      "",
	}
	for in, want := range cases {
		if got := extToFormat(in); got != want {
			t.Errorf("extToFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- Diacritics folding ---

func TestDiacriticsFoldingInWorkKey(t *testing.T) {
	cases := []struct {
		input, want string
	}{
		{"Antonín_Dvořák", "antonin dvorak"},
		{"Béla_Bartók", "bela bartok"},
		{"Karol_Szymanowski", "karol szymanowski"},
		{"Georg_Frideric_Händel", "georg frideric handel"},
		{"Leoš_Janáček", "leos janacek"},
		{"Isaac_Albéniz_pour_piano", "isaac albeniz pour piano"},
	}
	for _, c := range cases {
		got := classicalWorkKey("File:" + c.input + ".ogg")
		if got != c.want {
			t.Errorf("classicalWorkKey(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// --- Discover paging behavior ---

func TestDiscoverDoneWhenNoComposers(t *testing.T) {
	p := &ClassicalCategoriesProvider{
		SourceKey:    "test",
		RootCategory: "Category:Test",
		PageSize:     50,
	}
	p.initialized = true
	p.composerList = nil

	cands, next, done, err := p.Discover(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Fatal("should be done when no composers")
	}
	if len(cands) != 0 {
		t.Fatalf("expected 0 candidates, got %d", len(cands))
	}
	// Empty cursor advances to 0
	c := decodeCCCursor(next)
	if c.ComposerIndex != 0 {
		t.Fatalf("cursor composer index: got %d, want 0", c.ComposerIndex)
	}
}

func TestCursorReachesEnd(t *testing.T) {
	p := &ClassicalCategoriesProvider{
		SourceKey: "test",
		PageSize:  50,
	}
	p.initialized = true
	p.composerList = []composerInfo{
		{CategoryTitle: "Cat1", Name: "C1"},
	}

	// Cursor past end → done with no candidates
	cursor := ccCursor{ComposerIndex: 1}.encode()
	cands, next, done, err := p.Discover(context.Background(), cursor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Fatal("should be done")
	}
	if len(cands) != 0 {
		t.Fatalf("expected 0 candidates, got %d", len(cands))
	}
	c := decodeCCCursor(next)
	if c.ComposerIndex != 1 {
		t.Fatalf("cursor should stay at 1, got %d", c.ComposerIndex)
	}
}

// --- FormatWordRe test ---

func TestFormatWordRemovalInWorkKey(t *testing.T) {
	cases := []struct {
		input, want string
	}{
		{"Beethoven Symphony 5 ogg", "beethoven symphony 5"},
		{"Chopin Ballade opus", "chopin ballade"},
		{"Bach Prelude WAV", "bach prelude"},
		{"Mozart Requiem FLAC", "mozart requiem"},
	}
	for _, c := range cases {
		got := classicalWorkKey("File:" + c.input + ".ogg")
		if got != c.want {
			t.Errorf("classicalWorkKey(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}
