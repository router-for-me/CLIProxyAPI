package cmd

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestSetupOptions_ContainsCursorLogin(t *testing.T) {
	options := setupOptions()
	found := false
	for _, option := range options {
		if option.label == "Cursor login" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected setup options to include Cursor login")
	}
}

func TestSetupOptions_ContainsPromotedProviders(t *testing.T) {
	options := setupOptions()
	found := map[string]bool{
		"Cline API key login":   false,
		"AMP API key login":     false,
		"Factory API key login": false,
	}
	for _, option := range options {
		if _, ok := found[option.label]; ok {
			found[option.label] = true
		}
	}
	for label, ok := range found {
		if !ok {
			t.Fatalf("expected setup options to include %q", label)
		}
	}
}

func TestPrintPostCheckSummary_IncludesCursorProviderCount(t *testing.T) {
	cfg := &config.Config{
		CursorKey: []config.CursorKey{{CursorAPIURL: defaultCursorAPIURL}},
	}

	output := captureStdout(t, func() {
		printPostCheckSummary(cfg)
	})

	if !strings.Contains(output, "cursor=1") {
		t.Fatalf("summary output missing cursor count: %q", output)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	origStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = write
	fn()
	_ = write.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, read)
	_ = read.Close()

	return buf.String()
}
