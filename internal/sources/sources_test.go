package sources

import (
	"testing"

	"github.com/johnarleyburns/parso-pdaudio/internal/provider"
)

func TestResolveAll(t *testing.T) {
	keys, err := Resolve([]string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 11 {
		t.Fatalf("want 11 sources, got %d: %v", len(keys), keys)
	}
	if keys[0] != "chopin" {
		t.Fatalf("first source should be chopin, got %q", keys[0])
	}
}

func TestResolveSubsetAndUnknown(t *testing.T) {
	keys, err := Resolve([]string{"chopin", "marine"})
	if err != nil || len(keys) != 2 {
		t.Fatalf("subset failed: %v err=%v", keys, err)
	}
	if _, err := Resolve([]string{"nope"}); err == nil {
		t.Fatal("expected error for unknown source")
	}
}

func TestBuildProviders(t *testing.T) {
	client := provider.NewClient("test/1.0")
	provs, err := Build(Keys(), client, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(provs) != 11 {
		t.Fatalf("want 11 providers, got %d", len(provs))
	}
	var ia, commons, classical int
	for _, p := range provs {
		switch p.(type) {
		case *provider.IAProvider:
			ia++
		case *provider.ClassicalCategoriesProvider:
			classical++
		case *provider.CommonsProvider:
			commons++
		}
	}
	if ia != 3 || commons != 7 || classical != 1 {
		t.Fatalf("want 3 ia + 7 commons + 1 classical, got ia=%d commons=%d classical=%d", ia, commons, classical)
	}
}

func TestBuildClassicalProviderOptions(t *testing.T) {
	client := provider.NewClient("test/1.0")
	provs, err := Build([]string{"commons_classical"}, client, &BuildOpts{
		AllowAttribution: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provs) != 1 {
		t.Fatalf("want 1 provider, got %d", len(provs))
	}
	cp, ok := provs[0].(*provider.ClassicalCategoriesProvider)
	if !ok {
		t.Fatal("expected ClassicalCategoriesProvider")
	}
	if cp.LicensePolicy != "attribution" {
		t.Fatalf("LicensePolicy = %q, want %q", cp.LicensePolicy, "attribution")
	}
	if cp.RootCategory != "Category:Audio files of classical music by composer" {
		t.Fatalf("RootCategory = %q", cp.RootCategory)
	}
	if cp.MaxDepth != 3 {
		t.Fatalf("MaxDepth = %d, want 3", cp.MaxDepth)
	}
	if cp.MinBytes != 250000 {
		t.Fatalf("MinBytes = %d, want 250000", cp.MinBytes)
	}
	if len(cp.SkipSubcatPatterns) != 5 {
		t.Fatalf("SkipSubcatPatterns = %d items, want 5", len(cp.SkipSubcatPatterns))
	}

	// Test strict mode
	provs2, _ := Build([]string{"commons_classical"}, client, nil)
	cp2 := provs2[0].(*provider.ClassicalCategoriesProvider)
	if cp2.LicensePolicy != "strict" {
		t.Fatalf("default LicensePolicy = %q, want strict", cp2.LicensePolicy)
	}
}
