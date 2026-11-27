package pluginhost

import (
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// TestModelExecutionRequestFromPluginCarriesAction guards native actions across the host
// model callback: without the field the inner executor would take the chat path.
func TestModelExecutionRequestFromPluginCarriesAction(t *testing.T) {
	req := modelExecutionRequestFromPlugin(pluginapi.HostModelExecutionRequest{
		EntryProtocol: "gemini",
		ExitProtocol:  "gemini",
		Model:         "gemini-embedding-001",
		Action:        "batchEmbedContents",
	}, "router-plugin")
	if req.Action != "batchEmbedContents" {
		t.Fatalf("Action = %q, want batchEmbedContents", req.Action)
	}
	if req.SkipInterceptorPluginID != "router-plugin" || req.SkipRouterPluginID != "router-plugin" {
		t.Fatalf("skip ids not preserved: %+v", req)
	}
}

// TestHostModelExecutionRequestActionRoundTrip keeps the plugin-facing JSON contract stable.
func TestHostModelExecutionRequestActionRoundTrip(t *testing.T) {
	raw, err := json.Marshal(pluginapi.HostModelExecutionRequest{Action: "decisions"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var decoded pluginapi.HostModelExecutionRequest
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if decoded.Action != "decisions" {
		t.Fatalf("Action = %q, want decisions", decoded.Action)
	}

	// Omitting the action keeps chat callers wire-compatible.
	raw, err = json.Marshal(pluginapi.HostModelExecutionRequest{})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if ojson.NewDoc(raw).Get("action").Exists() {
		t.Fatal("empty action must be omitted from the wire format")
	}
}
