package auth

import (
	"encoding/json"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestHomeDispatchWebSearchCapability(t *testing.T) {
	for _, tc := range []struct {
		name, raw        string
		present, enabled bool
	}{
		{"true", `{"id":"upstream","native_capabilities":{"web_search":true}}`, true, true},
		{"false", `{"id":"upstream","native_capabilities":{"web_search":false}}`, true, false},
		{"old home", `{"id":"upstream"}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire homeDispatchModelInfo
			if err := json.Unmarshal([]byte(tc.raw), &wire); err != nil {
				t.Fatal(err)
			}
			req := attachResolvedHomeModelInfo(cliproxyexecutor.Request{Model: "alias"}, wire.registryModelInfo())
			info, ok := ResolvedModelInfo(req)
			if !ok || info.ID != "upstream" {
				t.Fatalf("missing resolved model: %+v", info)
			}
			present := info.NativeCapabilities != nil && info.NativeCapabilities.WebSearch != nil
			if present != tc.present {
				t.Fatalf("presence = %v", present)
			}
			if present && *info.NativeCapabilities.WebSearch != tc.enabled {
				t.Fatal("capability changed")
			}
		})
	}
}
