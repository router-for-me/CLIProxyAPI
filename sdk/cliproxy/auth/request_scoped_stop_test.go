package auth

import (
	"context"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestStoppedByOAuthRequestScopedRuleFollowsEffectiveConfig(t *testing.T) {
	contentFilter := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "{\n  \"error\": {\n    \"code\": 429,\n    \"message\": \"Resource has been exhausted (e.g. check quota).\",\n    \"status\": \"RESOURCE_EXHAUSTED\"\n  }\n}\n"}
	quota := &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"reason":"QUOTA_EXHAUSTED"}]}}`}

	cases := map[string]struct {
		yaml     string
		provider string
		err      error
		want     bool
	}{
		"default rule":      {yaml: "port: 8317", provider: "antigravity", err: contentFilter, want: true},
		"quota 429":         {yaml: "port: 8317", provider: "antigravity", err: quota, want: false},
		"other provider":    {yaml: "port: 8317", provider: "claude", err: contentFilter, want: false},
		"explicit opt-out":  {yaml: "port: 8317\noauth-request-scoped-errors:\n  antigravity: []\n", provider: "antigravity", err: contentFilter, want: false},
		"continue override": {yaml: "port: 8317\noauth-request-scoped-errors:\n  antigravity:\n    - status: 429\n      match: [RESOURCE_EXHAUSTED]\n      action: continue\n", provider: "antigravity", err: contentFilter, want: false},
	}
	for name, tc := range cases {
		cfg, errParse := internalconfig.ParseConfigBytes([]byte(tc.yaml))
		if errParse != nil {
			t.Fatalf("%s: ParseConfigBytes: %v", name, errParse)
		}
		manager := NewManager(nil, nil, nil)
		manager.SetConfig(cfg)
		if got := manager.StoppedByOAuthRequestScopedRule(context.Background(), tc.provider, tc.err); got != tc.want {
			t.Fatalf("%s: StoppedByOAuthRequestScopedRule = %v, want %v", name, got, tc.want)
		}
	}
}
