package config

import "testing"

func TestSanitizeOpenAICompatibility_NormalizesEndpointMode(t *testing.T) {
	cfg := &Config{
		OpenAICompatibility: []OpenAICompatibility{
			{
				Name:         "provider-a",
				BaseURL:      "https://example.com/v1",
				EndpointMode: " RESPONSES ",
			},
			{
				Name:         "provider-b",
				BaseURL:      "https://example.com/v2",
				EndpointMode: "unexpected",
			},
			{
				Name:         "provider-c",
				BaseURL:      "https://example.com/v3",
				EndpointMode: " upstream ",
			},
		},
	}

	cfg.SanitizeOpenAICompatibility()

	if got := cfg.OpenAICompatibility[0].EndpointMode; got != "responses" {
		t.Fatalf("endpoint mode = %q, want %q", got, "responses")
	}
	if got := cfg.OpenAICompatibility[1].EndpointMode; got != "" {
		t.Fatalf("endpoint mode = %q, want empty default mode", got)
	}
	if got := cfg.OpenAICompatibility[2].EndpointMode; got != "" {
		t.Fatalf("endpoint mode = %q, want empty default mode", got)
	}
}
