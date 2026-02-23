package claude

import (
	"strings"
	"testing"
)

func TestIsValidURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "valid https", url: "https://console.anthropic.com/", want: true},
		{name: "valid http", url: "http://localhost:3000/callback", want: true},
		{name: "missing host", url: "https:///path-only", want: false},
		{name: "relative url", url: "/local/path", want: false},
		{name: "javascript url", url: "javascript:alert(1)", want: false},
		{name: "data url", url: "data:text/html,<script>alert(1)</script>", want: false},
		{name: "empty", url: "   ", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isValidURL(tt.url); got != tt.want {
				t.Fatalf("isValidURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestGenerateSuccessHTMLEscapesPlatformURL(t *testing.T) {
	server := NewOAuthServer(9999)
	malicious := `https://console.anthropic.com/" onclick="alert('xss')`

	rendered := server.generateSuccessHTML(true, malicious)

	if strings.Contains(rendered, malicious) {
		t.Fatalf("rendered html contains unescaped platform URL")
	}
	if strings.Contains(rendered, `onclick="alert('xss')`) {
		t.Fatalf("rendered html contains unescaped injected attribute")
	}
	if !strings.Contains(rendered, `https://console.anthropic.com/&#34; onclick=&#34;alert(&#39;xss&#39;)`) {
		t.Fatalf("rendered html does not contain expected escaped URL")
	}
}
