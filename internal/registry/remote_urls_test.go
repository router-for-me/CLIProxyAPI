package registry

import (
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
