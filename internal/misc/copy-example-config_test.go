package misc

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCopyConfigTemplateUsesRemoteTemplateWhenLocalTemplateMissing(t *testing.T) {
	const fallbackURL = "https://raw.githubusercontent.com/caidaoli/CLIProxyAPI/refs/heads/main/config.example.yaml"
	const remoteConfig = "port: 8317\napi-keys:\n  - test-key\n"

	oldTransport := http.DefaultTransport
	t.Cleanup(func() {
		http.DefaultTransport = oldTransport
	})

	requests := 0
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.String() != fallbackURL {
			return nil, fmt.Errorf("unexpected fallback URL: %s", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(remoteConfig)),
			Request:    req,
		}, nil
	})

	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "config.example.yaml")
	dst := filepath.Join(tmpDir, "config", "config.yaml")

	if err := CopyConfigTemplate(src, dst); err != nil {
		t.Fatalf("CopyConfigTemplate() error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("fallback request count = %d, want 1", requests)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("failed to read copied config: %v", err)
	}
	if string(got) != remoteConfig {
		t.Fatalf("copied config = %q, want %q", string(got), remoteConfig)
	}
}
