package antigravity

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAPIUserAgent_ReturnsAntigravityFormat(t *testing.T) {
	ua := APIUserAgent()
	if !strings.HasPrefix(ua, "antigravity/") {
		t.Errorf("APIUserAgent() should start with 'antigravity/', got %q", ua)
	}
	if !strings.Contains(ua, "darwin/arm64") {
		t.Errorf("APIUserAgent() should contain platform, got %q", ua)
	}
}

func TestAPIUserAgent_NeverReturnsNodeJSClient(t *testing.T) {
	ua := APIUserAgent()
	if strings.Contains(ua, "google-api-nodejs-client") {
		t.Errorf("APIUserAgent() must not return nodejs client UA, got %q", ua)
	}
	if strings.Contains(ua, "Go-http-client") {
		t.Errorf("APIUserAgent() must not return Go default UA, got %q", ua)
	}
}

func TestAPIUserAgent_NotEmpty(t *testing.T) {
	ua := APIUserAgent()
	if strings.TrimSpace(ua) == "" {
		t.Error("APIUserAgent() must not return empty string")
	}
}

func TestClientMetadata_IsValidJSON(t *testing.T) {
	var m map[string]string
	if err := json.Unmarshal([]byte(ClientMetadata), &m); err != nil {
		t.Fatalf("ClientMetadata is not valid JSON: %v", err)
	}
}

func TestClientMetadata_HasAntigravityIdeType(t *testing.T) {
	var m map[string]string
	if err := json.Unmarshal([]byte(ClientMetadata), &m); err != nil {
		t.Fatalf("ClientMetadata is not valid JSON: %v", err)
	}

	ideType, ok := m["ideType"]
	if !ok {
		t.Fatal("ClientMetadata missing ideType field")
	}
	if ideType != "ANTIGRAVITY" {
		t.Errorf("ideType should be 'ANTIGRAVITY', got %q", ideType)
	}
}

func TestClientMetadata_HasPluginTypeGemini(t *testing.T) {
	var m map[string]string
	if err := json.Unmarshal([]byte(ClientMetadata), &m); err != nil {
		t.Fatalf("ClientMetadata is not valid JSON: %v", err)
	}

	pluginType, ok := m["pluginType"]
	if !ok {
		t.Fatal("ClientMetadata missing pluginType field")
	}
	if pluginType != "GEMINI" {
		t.Errorf("pluginType should be 'GEMINI', got %q", pluginType)
	}
}

func TestClientMetadata_DoesNotContainIDEUnspecified(t *testing.T) {
	if strings.Contains(ClientMetadata, "IDE_UNSPECIFIED") {
		t.Error("ClientMetadata should not contain IDE_UNSPECIFIED (that's for gemini-cli, not Antigravity)")
	}
}

func TestOAuthConstants_NotEmpty(t *testing.T) {
	if ClientID == "" {
		t.Error("ClientID must not be empty")
	}
	if ClientSecret == "" {
		t.Error("ClientSecret must not be empty")
	}
	if TokenEndpoint == "" {
		t.Error("TokenEndpoint must not be empty")
	}
	if APIEndpoint == "" {
		t.Error("APIEndpoint must not be empty")
	}
}

func TestScopes_ContainsCloudPlatform(t *testing.T) {
	found := false
	for _, s := range Scopes {
		if strings.Contains(s, "cloud-platform") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Scopes should contain cloud-platform scope")
	}
}
