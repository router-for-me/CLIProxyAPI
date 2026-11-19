package auth

import (
	"context"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestStoppedByRequestScopedRuleUsesEffectiveRules(t *testing.T) {
	contentFilter := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "{\n  \"error\": {\n    \"code\": 429,\n    \"message\": \"Resource has been exhausted (e.g. check quota).\",\n    \"status\": \"RESOURCE_EXHAUSTED\"\n  }\n}\n"}
	quota := &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"reason":"QUOTA_EXHAUSTED"}]}}`}
	oauth := func(provider string, metadata map[string]any) *Auth {
		return &Auth{ID: provider + "-1", Provider: provider, Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth}, Metadata: metadata}
	}
	continueOverride := map[string]any{"request_scoped_errors": []internalconfig.RequestScopedErrorRule{{
		Status: http.StatusTooManyRequests, Match: []string{"RESOURCE_EXHAUSTED"}, Action: RequestScopedActionContinue,
	}}}

	cases := map[string]struct {
		yaml string
		auth *Auth
		err  error
		want bool
	}{
		"default rule":              {yaml: "port: 8317", auth: oauth("antigravity", nil), err: contentFilter, want: true},
		"quota 429":                 {yaml: "port: 8317", auth: oauth("antigravity", nil), err: quota, want: false},
		"other provider":            {yaml: "port: 8317", auth: oauth("claude", nil), err: contentFilter, want: false},
		"explicit opt-out":          {yaml: "port: 8317\noauth-request-scoped-errors:\n  antigravity: []\n", auth: oauth("antigravity", nil), err: contentFilter, want: false},
		"per-credential continue":   {yaml: "port: 8317", auth: oauth("antigravity", continueOverride), err: contentFilter, want: false},
		"no auth":                   {yaml: "port: 8317", auth: nil, err: contentFilter, want: false},
		"non-oauth antigravity key": {yaml: "port: 8317", auth: &Auth{ID: "k", Provider: "antigravity", Attributes: map[string]string{AttributeAPIKey: "k"}}, err: contentFilter, want: false},
	}
	for name, tc := range cases {
		cfg, errParse := internalconfig.ParseConfigBytes([]byte(tc.yaml))
		if errParse != nil {
			t.Fatalf("%s: ParseConfigBytes: %v", name, errParse)
		}
		manager := NewManager(nil, nil, nil)
		manager.SetConfig(cfg)
		if got := manager.StoppedByRequestScopedRule(context.Background(), tc.auth, tc.err); got != tc.want {
			t.Fatalf("%s: StoppedByRequestScopedRule = %v, want %v", name, got, tc.want)
		}
	}
}

func TestPublishSelectedAuthMetadataReportsSnapshot(t *testing.T) {
	var got *Auth
	selected := &Auth{ID: "auth-1", Provider: "antigravity"}
	publishSelectedAuthMetadata(map[string]any{SelectedAuthSnapshotCallbackMetadataKey: func(auth *Auth) { got = auth }}, selected)
	if got == nil || got == selected || got.ID != "auth-1" || got.Provider != "antigravity" {
		t.Fatalf("snapshot = %#v, want a copy of the selected auth", got)
	}
}
