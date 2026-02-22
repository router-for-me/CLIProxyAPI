package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
)

func TestValidateKiroIncognitoFlags(t *testing.T) {
	if err := validateKiroIncognitoFlags(false, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateKiroIncognitoFlags(true, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateKiroIncognitoFlags(false, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateKiroIncognitoFlags(true, true); err == nil {
		t.Fatal("expected conflict error when both flags are set")
	}
}

func TestSetKiroIncognitoMode(t *testing.T) {
	cfg := &config.Config{}

	setKiroIncognitoMode(cfg, false, false)
	if !cfg.IncognitoBrowser {
		t.Fatal("expected default Kiro mode to enable incognito")
	}

	setKiroIncognitoMode(cfg, false, true)
	if cfg.IncognitoBrowser {
		t.Fatal("expected --no-incognito to disable incognito")
	}

	setKiroIncognitoMode(cfg, true, false)
	if !cfg.IncognitoBrowser {
		t.Fatal("expected --incognito to enable incognito")
	}
}
