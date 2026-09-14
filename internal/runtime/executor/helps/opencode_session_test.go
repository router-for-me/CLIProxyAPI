package helps

import (
	"net/http"
	"testing"
)

func TestApplyOpenCodeSessionHeaders(t *testing.T) {
	t.Parallel()

	const goURL = "https://opencode.ai/zen/go/v1"

	tests := []struct {
		name      string
		targetURL string
		provider  string
		incoming  http.Header
		existing  string
		sessionID string
		want      string
	}{
		{
			name:      "go gateway synthesizes from the routing session",
			targetURL: goURL,
			sessionID: "affinity:ses_routing",
			want:      "ses_routing",
		},
		{
			name:      "mirror named after the provider",
			targetURL: "http://127.0.0.1:8080/v1",
			provider:  "opencode-go",
			incoming:  http.Header{"X-Session-Affinity": []string{"ses_mirror"}},
			sessionID: "affinity:ses_mirror",
			want:      "ses_mirror",
		},
		{
			name:      "client header beats the routing session",
			targetURL: goURL,
			incoming:  http.Header{"X-Opencode-Session": []string{"ses_native"}},
			sessionID: "affinity:ses_routing",
			want:      "ses_native",
		},
		{
			name:      "configured header wins",
			targetURL: goURL,
			incoming:  http.Header{"X-Opencode-Session": []string{"ses_native"}},
			existing:  "pinned",
			sessionID: "affinity:ses_routing",
			want:      "pinned",
		},
		{
			name:      "lookalike host receives nothing",
			targetURL: "https://evilopencode.ai/v1",
			sessionID: "affinity:ses_leak",
			want:      "",
		},
		{
			name:      "unrelated upstream receives nothing",
			targetURL: "https://openrouter.ai/api/v1",
			provider:  "openrouter",
			incoming:  http.Header{"X-Session-Affinity": []string{"ses_leak"}},
			sessionID: "affinity:ses_leak",
			want:      "",
		},
		{
			name:      "no session signal leaves the header unset",
			targetURL: goURL,
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req, errReq := http.NewRequest(http.MethodPost, tt.targetURL+"/chat/completions", nil)
			if errReq != nil {
				t.Fatalf("new request: %v", errReq)
			}
			if tt.existing != "" {
				req.Header.Set(openCodeSessionHeader, tt.existing)
			}

			ApplyOpenCodeSessionHeaders(req, tt.targetURL, tt.provider, tt.incoming, tt.sessionID)

			if got := req.Header.Get(openCodeSessionHeader); got != tt.want {
				t.Fatalf("%s = %q, want %q", openCodeSessionHeader, got, tt.want)
			}
		})
	}
}
