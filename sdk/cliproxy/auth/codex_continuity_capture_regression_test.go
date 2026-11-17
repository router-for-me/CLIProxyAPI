package auth

import (
	"context"
	"reflect"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestCodexQuotaCodeContinuityProvenance(t *testing.T) {
	for _, scope := range []string{"model", "auth"} {
		for _, code := range []string{"usage_limit_reached", "quota_exceeded"} {
			t.Run(scope+"/"+code, func(t *testing.T) {
				for _, responseBody := range []bool{false, true} {
					func() {
						withQuotaCooldownEnabled(t)
						ctx := context.Background()
						cfg := &internalconfig.Config{}
						cfg.Codex.ModelLevelCooling = true
						m := NewManager(nil, nil, nil)
						m.SetConfig(cfg)
						a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive})
						if err != nil {
							t.Fatal(err)
						}
						model := ""
						if scope == "model" {
							model = "gpt-5-codex"
						}
						quotaErr := &Error{HTTPStatus: 429, Message: `{"error":{"code":"` + code + `"}}`}
						if responseBody {
							source := codexResponseBodyQuotaErr{status: 429, body: []byte(quotaErr.Message)}
							if !isPositiveQuotaRejectionError(source) {
								t.Fatal("fixture source is not a positive continuity quota rejection")
							}
							quotaErr = resultErrorFromError(source)
							if quotaErr.Code != "" || quotaErr.Message != "upstream 429" || quotaErr.HTTPStatus != 429 {
								t.Fatalf("response-body code conversion changed fields: %#v", quotaErr)
							}
							if quotaErr.Metadata[positiveQuotaRejectionMetadataKey] != "true" {
								t.Error("lost original response-body quota classification")
							}

							existing := &Error{Code: "model_cooldown", Message: "preserve message", HTTPStatus: 429, Retryable: true, Metadata: map[string]string{"provider": "preserve detail"}}
							preserved := resultErrorFromError(WithCause(existing, source))
							if !reflect.DeepEqual(preserved, existing) {
								t.Fatalf("existing classification changed: got %#v want %#v", preserved, existing)
							}
						}
						if !responseBody && !isPositiveQuotaRejectionError(quotaErr) {
							t.Fatal("fixture is not a positive continuity quota rejection")
						}
						duration := time.Minute
						m.MarkResult(ctx, Result{AuthID: a.ID, Provider: "codex", Model: model, Error: quotaErr, RetryAfter: &duration})
						got, _ := m.GetByID(a.ID)
						if !got.nonQuotaCooldownUntil.IsZero() {
							t.Error("positive quota rejection acquired an auth-level independent deadline")
						}
						if model != "" {
							state := got.ModelStates[model]
							if state == nil || !state.Quota.Exceeded {
								t.Fatal("model quota restriction missing")
							}
							if !state.nonQuotaCooldownUntil.IsZero() {
								t.Error("positive quota rejection acquired a model-level independent deadline")
							}
						}
						if !isQuotaCooldownBlock(got, model, time.Now()) {
							t.Error("positive quota rejection became ineligible for quota continuity")
						}
						if QuotaRecoveryCandidate(got) {
							t.Fatal("continuity classification broadened subscription reset eligibility")
						}
					}()
				}
			})
		}
	}
}
