package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testConfigPersister struct {
	calls int
}

func (p *testConfigPersister) PersistConfig(context.Context) error {
	p.calls++
	return nil
}

type testRoundTripFunc func(*http.Request) (*http.Response, error)

func (f testRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestBootstrapGitBackedConfigUsesRemoteTemplateWhenLocalTemplateMissing(t *testing.T) {
	const fallbackURL = "https://raw.githubusercontent.com/caidaoli/CLIProxyAPI/refs/heads/main/config.example.yaml"
	const remoteConfig = "port: 8317\n"

	oldTransport := http.DefaultTransport
	t.Cleanup(func() {
		http.DefaultTransport = oldTransport
	})

	requests := 0
	http.DefaultTransport = testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
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
	persister := &testConfigPersister{}
	configPath := filepath.Join(tmpDir, "gitstore", "config", "config.yaml")
	examplePath := filepath.Join(tmpDir, "config.example.yaml")

	if err := bootstrapGitBackedConfig(context.Background(), examplePath, configPath, persister); err != nil {
		t.Fatalf("bootstrapGitBackedConfig() error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("fallback request count = %d, want 1", requests)
	}
	if persister.calls != 1 {
		t.Fatalf("PersistConfig calls = %d, want 1", persister.calls)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	if string(got) != remoteConfig {
		t.Fatalf("config content = %q, want %q", string(got), remoteConfig)
	}
}
