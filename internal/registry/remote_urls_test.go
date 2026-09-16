package registry

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolveRemoteURLsWithoutOverride(t *testing.T) {
	t.Setenv("CPA_TEST_REMOTE_URLS", "")
	defaults := []string{"https://default.example/a.json", "https://default.example/b.json"}
	got := resolveRemoteURLs("CPA_TEST_REMOTE_URLS", defaults)
	if !reflect.DeepEqual(got, defaults) {
		t.Fatalf("got %v, want %v", got, defaults)
	}
}

func TestResolveRemoteURLsPrependsTrimmedCustomList(t *testing.T) {
	t.Setenv("CPA_TEST_REMOTE_URLS", " https://one.example/x.json ,, https://two.example/y.json ,")
	got := resolveRemoteURLs("CPA_TEST_REMOTE_URLS", []string{"https://default.example/m.json"})
	want := []string{
		"https://one.example/x.json",
		"https://two.example/y.json",
		"https://default.example/m.json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestIsHTTPSource(t *testing.T) {
	cases := map[string]bool{
		"http://x/a.json":           true,
		"https://x/a.json":          true,
		"HTTPS://X/A.JSON":          true,
		"C:\\catalogs\\m.json":      false,
		"file://C:/catalogs/m.json": false,
		"./models.json":             false,
	}
	for source, want := range cases {
		if got := isHTTPSource(source); got != want {
			t.Errorf("isHTTPSource(%q) = %v, want %v", source, got, want)
		}
	}
}

func TestReadLocalCatalog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.json")
	content := []byte("{}")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readLocalCatalog(path)
	if err != nil || string(got) != string(content) {
		t.Fatalf("plain path: got %q, err %v", got, err)
	}
	uri := "file://" + filepath.ToSlash(path)
	if _, err := readLocalCatalog(uri); err != nil {
		t.Errorf("file URI (%s): unexpected error %v", uri, err)
	}
	if _, err := readLocalCatalog(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("expected error for missing local catalog")
	}
}
