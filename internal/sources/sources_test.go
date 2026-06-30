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
	if len(keys) != 10 {
		t.Fatalf("want 10 sources, got %d: %v", len(keys), keys)
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
	provs, err := Build(Keys(), client)
	if err != nil {
		t.Fatal(err)
	}
	if len(provs) != 10 {
		t.Fatalf("want 10 providers, got %d", len(provs))
	}
	var ia, commons int
	for _, p := range provs {
		switch p.(type) {
		case *provider.IAProvider:
			ia++
		case *provider.CommonsProvider:
			commons++
		}
	}
	if ia != 3 || commons != 7 {
		t.Fatalf("want 3 ia + 7 commons, got ia=%d commons=%d", ia, commons)
	}
}
