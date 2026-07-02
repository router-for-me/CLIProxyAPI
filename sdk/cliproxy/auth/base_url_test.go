package auth

import "testing"

func TestBaseURLFromMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]any
		want     string
	}{
		{name: "underscore", metadata: map[string]any{"base_url": " http://127.0.0.1:18081 "}, want: "http://127.0.0.1:18081"},
		{name: "dash alias", metadata: map[string]any{"base-url": "http://127.0.0.1:18082"}, want: "http://127.0.0.1:18082"},
		{name: "underscore wins", metadata: map[string]any{"base_url": "http://a", "base-url": "http://b"}, want: "http://a"},
		{name: "empty", metadata: map[string]any{"base_url": "  "}, want: ""},
		{name: "wrong type", metadata: map[string]any{"base_url": 123}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BaseURLFromMetadata(tt.metadata); got != tt.want {
				t.Fatalf("BaseURLFromMetadata() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApplyBaseURLFromMetadata(t *testing.T) {
	auth := &Auth{
		Provider: "claude",
		Metadata: map[string]any{"base_url": "http://127.0.0.1:18081", "access_token": "oauth-token"},
	}
	ApplyBaseURLFromMetadata(auth)
	if got := auth.Attributes["base_url"]; got != "http://127.0.0.1:18081" {
		t.Fatalf("base_url attr = %q, want copied metadata base URL", got)
	}
}

func TestApplyBaseURLFromMetadataSkipsPluginProvider(t *testing.T) {
	auth := &Auth{
		Provider: "my-plugin",
		Metadata: map[string]any{"base_url": "http://127.0.0.1:18081", "access_token": "oauth-token"},
	}
	ApplyBaseURLFromMetadata(auth)
	if _, ok := auth.Attributes["base_url"]; ok {
		t.Fatalf("base_url attr should not be copied for plugin provider")
	}
}

func TestApplyBaseURLFromMetadataSkipsAPIKeyAuth(t *testing.T) {
	auth := &Auth{
		Provider:   "claude",
		Attributes: map[string]string{AttributeAPIKey: "sk-ant-test"},
		Metadata:   map[string]any{"base_url": "http://127.0.0.1:18081"},
	}
	ApplyBaseURLFromMetadata(auth)
	if _, ok := auth.Attributes["base_url"]; ok {
		t.Fatalf("base_url attr should not be copied for API-key auth")
	}
}
