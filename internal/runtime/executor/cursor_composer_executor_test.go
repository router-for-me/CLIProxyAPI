package executor

import (
	"bytes"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCursorComposerCredentialsDefaults(t *testing.T) {
	t.Setenv("CURSOR_BACKEND_BASE_URL", "")
	t.Setenv("CURSOR_CHAT_ENDPOINT", "")
	t.Setenv("CURSOR_CLIENT_VERSION", "")

	_, _, backend, endpoint, version := cursorComposerCredentials(&cliproxyauth.Auth{
		Attributes: map[string]string{"api_key": "crsr_test"},
	})
	if backend == "" {
		t.Fatal("expected default backend base URL")
	}
	if endpoint == "" {
		t.Fatal("expected default chat endpoint")
	}
	if version == "" {
		t.Fatal("expected default client version")
	}
}

func TestCursorComposerCredentialsAttrOverride(t *testing.T) {
	t.Setenv("CURSOR_BACKEND_BASE_URL", "https://env.example")
	_, _, backend, endpoint, version := cursorComposerCredentials(&cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":          "crsr_test",
			"backend_base_url": "https://cfg.example",
			"chat_endpoint":    "custom.Endpoint/Run",
			"client_version":   "9.9.9",
		},
	})
	if backend != "https://cfg.example" {
		t.Fatalf("backend = %q, want https://cfg.example", backend)
	}
	if endpoint != "custom.Endpoint/Run" {
		t.Fatalf("endpoint = %q, want custom.Endpoint/Run", endpoint)
	}
	if version != "9.9.9" {
		t.Fatalf("version = %q, want 9.9.9", version)
	}
}

func TestCursorSessionIDUsesAccessTokenHash(t *testing.T) {
	got := cursorSessionID("access-token-example")
	wantPrefix := sha256Hex("access-token-example")[:8]
	if len(got) < 8 || got[:8] != wantPrefix {
		t.Fatalf("cursorSessionID() = %q, want prefix %q", got, wantPrefix)
	}
	if got == stableUUID("", "access-token-example") {
		t.Fatal("cursorSessionID must not use stableUUID with empty namespace")
	}
}

func TestCursorComposerSDKBridgeURLEmptyWhenUnset(t *testing.T) {
	got := cursorComposerSDKBridgeURL(&cliproxyauth.Auth{Attributes: map[string]string{"api_key": "crsr_test"}})
	if got != "" {
		t.Fatalf("cursorComposerSDKBridgeURL() = %q, want empty string", got)
	}
}

func TestCursorComposerSDKBridgeURLNormalizesExplicitValue(t *testing.T) {
	got := cursorComposerSDKBridgeURL(&cliproxyauth.Auth{Attributes: map[string]string{"sdk_bridge_url": "http://127.0.0.1:8792"}})
	if got != "http://127.0.0.1:8792/sdk" {
		t.Fatalf("cursorComposerSDKBridgeURL() = %q, want http://127.0.0.1:8792/sdk", got)
	}
}

func TestReadProtoVarintOverflow(t *testing.T) {
	_, _, err := readProtoVarint(bytes.Repeat([]byte{0x80}, 11), 0)
	if err == nil {
		t.Fatal("readProtoVarint() expected overflow error")
	}
	if !strings.Contains(err.Error(), "varint overflow") {
		t.Fatalf("readProtoVarint() error = %v, want varint overflow", err)
	}
}

func TestParseConnectProtoFramesStopsAfterEndStream(t *testing.T) {
	endStreamPayload := []byte(`{"error":{"message":"boom"}}`)
	endStreamFrame := encodeConnectFrame(endStreamPayload)
	endStreamFrame[0] = 2
	followUpFrame := encodeConnectFrame([]byte("late-frame"))
	stream := bytes.NewReader(append(endStreamFrame, followUpFrame...))

	var gotErr error
	var payloads [][]byte
	for frame, err := range parseConnectProtoFrames(stream) {
		if err != nil {
			gotErr = err
			continue
		}
		payloads = append(payloads, append([]byte(nil), frame...))
	}

	if gotErr == nil {
		t.Fatal("parseConnectProtoFrames() expected error from end stream frame")
	}
	if !strings.Contains(gotErr.Error(), "boom") {
		t.Fatalf("parseConnectProtoFrames() error = %v, want boom", gotErr)
	}
	if len(payloads) != 0 {
		t.Fatalf("parseConnectProtoFrames() yielded %d payload(s), want 0", len(payloads))
	}
}
