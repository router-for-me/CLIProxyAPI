package registry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPriceCatalogUpdaterFileSourceKeepsLastGood(t *testing.T) {
	var published [][]byte
	validate := func(data []byte) error {
		if string(data) == "bad" {
			return errors.New("invalid")
		}
		return nil
	}
	p := NewPriceCatalogUpdater([]byte("embedded"), nil, validate, func(data []byte) error {
		published = append(published, data)
		return nil
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "prices.json")
	ctx := context.Background()

	if err := os.WriteFile(path, []byte("good"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.updater.refresh(ctx, path, p.updater.generation)
	if len(published) != 1 || string(published[0]) != "good" {
		t.Fatalf("published = %q, want [good]", published)
	}

	// An invalid file is rejected before publish: the last good catalog stays.
	if err := os.WriteFile(path, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.updater.refresh(ctx, path, p.updater.generation)
	if len(published) != 1 {
		t.Fatalf("invalid catalog was published: %q", published)
	}

	// A missing file also keeps the last good catalog.
	p.updater.refresh(ctx, filepath.Join(dir, "missing.json"), p.updater.generation)
	if len(published) != 1 {
		t.Fatalf("missing catalog was published: %q", published)
	}

	p.updater.refresh(ctx, embeddedCatalogSource, p.updater.generation)
	if len(published) != 2 || string(published[1]) != "embedded" {
		t.Fatalf("embedded source not published: %q", published)
	}
}
