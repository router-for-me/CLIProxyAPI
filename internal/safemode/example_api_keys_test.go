package safemode

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestExampleAPIKeysDetectsOnlyTemplateValues(t *testing.T) {
	keys := []string{
		" real-key ",
		" your-api-key-1 ",
		"your-api-key",
		"change-me",
		"your-api-key-2",
		"your-api-key-2",
		"your-api-key-3",
	}

	got := ExampleAPIKeys(keys)
	want := []string{"your-api-key-1", "your-api-key-2", "your-api-key-3"}
	if len(got) != len(want) {
		t.Fatalf("ExampleAPIKeys() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ExampleAPIKeys()[%d] = %q, want %q (all: %#v)", i, got[i], want[i], got)
		}
	}
}

func TestExampleAPIKeysIgnoresSimilarValues(t *testing.T) {
	keys := []string{"your-api-key", "change-me", "changeme", "your-api-key-4", "my-your-api-key-1"}
	if got := ExampleAPIKeys(keys); len(got) != 0 {
		t.Fatalf("ExampleAPIKeys() = %#v, want empty", got)
	}
	if HasExampleAPIKeys(keys) {
		t.Fatal("HasExampleAPIKeys() = true, want false")
	}
}

func TestExampleAPIKeyWarningHandler(t *testing.T) {
	handler := NewExampleAPIKeyWarningHandler("C:\\config.yaml", []string{"your-api-key-1"})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", w.Code, http.StatusOK)
	}
	body := w.Body.String()
	for _, want := range []string{"Example API key detected", "your-api-key-1", "C:\\config.yaml"} {
		if !strings.Contains(body, want) {
			t.Fatalf("GET / body missing %q: %s", want, body)
		}
	}

	req = httptest.NewRequest(http.MethodHead, "/", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HEAD / status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("HEAD / body length = %d, want 0", w.Body.Len())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /v1/models status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestWarningServerURL(t *testing.T) {
	cfg := &config.Config{Port: 8317}
	if got := WarningServerURL(cfg); got != "http://127.0.0.1:8317/" {
		t.Fatalf("WarningServerURL() = %q", got)
	}

	cfg.Host = "::1"
	cfg.TLS.Enable = true
	if got := WarningServerURL(cfg); got != "https://[::1]:8317/" {
		t.Fatalf("WarningServerURL() = %q", got)
	}
}
