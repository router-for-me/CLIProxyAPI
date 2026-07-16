package helps

import "testing"

func TestNormalizeBaseURL(t *testing.T) {
	t.Parallel()

	if got := NormalizeBaseURL("  https://example.com/api///  "); got != "https://example.com/api" {
		t.Fatalf("NormalizeBaseURL() = %q, want %q", got, "https://example.com/api")
	}
}

func TestJoinBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		baseURL  string
		endpoint string
		want     string
	}{
		{
			name:     "no boundary slash",
			baseURL:  "https://example.com",
			endpoint: "v1/messages",
			want:     "https://example.com/v1/messages",
		},
		{
			name:     "single boundary slash",
			baseURL:  "https://example.com/",
			endpoint: "/v1/messages?beta=true",
			want:     "https://example.com/v1/messages?beta=true",
		},
		{
			name:     "multiple boundary slashes",
			baseURL:  "https://example.com/api///",
			endpoint: "///v1/messages",
			want:     "https://example.com/api/v1/messages",
		},
		{
			name:     "surrounding whitespace",
			baseURL:  "  https://example.com/root/  ",
			endpoint: "  /responses  ",
			want:     "https://example.com/root/responses",
		},
		{
			name:     "empty endpoint",
			baseURL:  "https://example.com/",
			endpoint: "",
			want:     "https://example.com",
		},
		{
			name:     "empty base URL",
			baseURL:  "",
			endpoint: "/v1/messages",
			want:     "/v1/messages",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := JoinBaseURL(tt.baseURL, tt.endpoint); got != tt.want {
				t.Fatalf("JoinBaseURL(%q, %q) = %q, want %q", tt.baseURL, tt.endpoint, got, tt.want)
			}
		})
	}
}
