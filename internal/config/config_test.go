package config

import "testing"

func hasFormat(list []string, f string) bool {
	for _, x := range list {
		if x == f {
			return true
		}
	}
	return false
}

func TestParseDefaults(t *testing.T) {
	c, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Dir != "./library" || c.OpusBitrate != 128 || c.Packager != "go" {
		t.Fatalf("bad defaults: %+v", c)
	}
	if len(c.Prefer) < 4 || c.Prefer[0] != "opus" || c.Prefer[3] != "mp3" {
		t.Fatalf("bad prefer: %v", c.Prefer)
	}
	if !c.CommonsAllowFlac {
		t.Fatal("CommonsAllowFlac should default to true")
	}
	if !hasFormat(c.Prefer, "flac") {
		t.Fatal("CommonsAllowFlac=true should add flac to Prefer")
	}
	if c.CommonsAllowAttribution {
		t.Fatal("CommonsAllowAttribution should default to false")
	}
	if c.MinDurationSec != 30 {
		t.Fatalf("MinDurationSec should default to 30, got %f", c.MinDurationSec)
	}
	if len(c.Sources) != 1 || c.Sources[0] != "all" {
		t.Fatalf("bad sources: %v", c.Sources)
	}
	if !c.CommonsAllowFlac {
		t.Fatal("CommonsAllowFlac should default to true")
	}
	if c.CommonsAllowAttribution {
		t.Fatal("CommonsAllowAttribution should default to false")
	}
	if c.MinDurationSec != 30 {
		t.Fatalf("MinDurationSec should default to 30, got %f", c.MinDurationSec)
	}
}

func TestParseOverridesAndValidation(t *testing.T) {
	c, err := Parse([]string{"--dir", "/tmp/x", "--sources", "chopin,marine",
		"--opus-bitrate", "96", "--packager", "ffmpeg", "--no-tui"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Dir != "/tmp/x" || c.OpusBitrate != 96 || c.Packager != "ffmpeg" || !c.NoTUI {
		t.Fatalf("override failed: %+v", c)
	}
	if len(c.Sources) != 2 {
		t.Fatalf("sources = %v", c.Sources)
	}
	if _, err := Parse([]string{"--packager", "bogus"}); err == nil {
		t.Fatal("expected invalid packager error")
	}
	if _, err := Parse([]string{"--opus-bitrate", "9000"}); err == nil {
		t.Fatal("expected invalid bitrate error")
	}
}

func TestParseCommonsFlags(t *testing.T) {
	// commons-allow-attribution
	c, err := Parse([]string{"--commons-allow-attribution"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.CommonsAllowAttribution {
		t.Fatal("CommonsAllowAttribution should be true")
	}
	// commons-allow-flac=false
	c, err = Parse([]string{"--commons-allow-flac=false"})
	if err != nil {
		t.Fatal(err)
	}
	if c.CommonsAllowFlac {
		t.Fatal("CommonsAllowFlac should be false")
	}
	// min-duration
	c, err = Parse([]string{"--min-duration", "60.5"})
	if err != nil {
		t.Fatal(err)
	}
	if c.MinDurationSec != 60.5 {
		t.Fatalf("MinDurationSec = %f, want 60.5", c.MinDurationSec)
	}
}

func TestLicenseAllowed(t *testing.T) {
	c, _ := Parse([]string{"--require-license", "cc0,pd-usgov"})
	if !c.LicenseAllowed("CC0", "http://creativecommons.org/publicdomain/zero/1.0/") {
		t.Fatal("cc0 should be allowed")
	}
	if !c.LicenseAllowed("PD-USGov", "") {
		t.Fatal("pd-usgov should be allowed")
	}
	if c.LicenseAllowed("CC BY-SA", "http://x/by-sa") {
		t.Fatal("cc by-sa should be rejected")
	}

	open, _ := Parse(nil)
	if !open.LicenseAllowed("anything", "") {
		t.Fatal("empty allowlist should allow all")
	}
}
