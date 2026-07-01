package config

import "testing"

func TestSanitizeCursorComposerKeysLeavesSDKBridgeURLEmptyWhenUnset(t *testing.T) {
	cfg := &Config{CursorComposerKey: make([]CursorComposerKey, 1)}
	cfg.CursorComposerKey[0].APIKey = "crsr_test"

	cfg.SanitizeCursorComposerKeys()

	if got := cfg.CursorComposerKey[0].SDKBridgeURL; got != "" {
		t.Fatalf("SDKBridgeURL = %q, want empty string", got)
	}
}

func TestSanitizeCursorComposerKeysKeepsExplicitSDKBridgeURL(t *testing.T) {
	cfg := &Config{CursorComposerKey: make([]CursorComposerKey, 1)}
	cfg.CursorComposerKey[0].APIKey = "crsr_test"
	cfg.CursorComposerKey[0].SDKBridgeURL = "  http://127.0.0.1:8792  "

	cfg.SanitizeCursorComposerKeys()

	if got := cfg.CursorComposerKey[0].SDKBridgeURL; got != "http://127.0.0.1:8792" {
		t.Fatalf("SDKBridgeURL = %q, want http://127.0.0.1:8792", got)
	}
}
