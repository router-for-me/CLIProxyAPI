package executor

import (
	"net/http"
	"strings"
	"testing"
)

func TestMetadataPlatformFromUserAgent(t *testing.T) {
	tests := []struct {
		name string
		ua   string
		want string
	}{
		{name: "windows ua", ua: "antigravity/1.104.0 windows/amd64", want: "WINDOWS"},
		{name: "darwin ua", ua: "antigravity/1.104.0 darwin/arm64", want: "MACOS"},
		{name: "unknown ua", ua: "antigravity/1.104.0 linux/amd64", want: "MACOS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := metadataPlatformFromUserAgent(tt.ua)
			if got != tt.want {
				t.Fatalf("metadataPlatformFromUserAgent(%q)=%q want=%q", tt.ua, got, tt.want)
			}
		})
	}
}

func TestApplyAntigravityHeaders(t *testing.T) {
	h := make(http.Header)
	applyAntigravityHeaders(h, nil)

	ua := h.Get("User-Agent")
	if ua == "" {
		t.Fatal("User-Agent should be set")
	}
	if !strings.HasPrefix(ua, "antigravity/") {
		t.Fatalf("unexpected User-Agent: %s", ua)
	}
	if h.Get("X-Goog-Api-Client") != antigravityAPIClient {
		t.Fatalf("unexpected X-Goog-Api-Client: %s", h.Get("X-Goog-Api-Client"))
	}
	metadata := h.Get("Client-Metadata")
	if !strings.Contains(metadata, `"ideType":"ANTIGRAVITY"`) {
		t.Fatalf("unexpected Client-Metadata: %s", metadata)
	}
	if !(strings.Contains(metadata, `"platform":"WINDOWS"`) || strings.Contains(metadata, `"platform":"MACOS"`)) {
		t.Fatalf("unexpected Client-Metadata platform: %s", metadata)
	}
}
