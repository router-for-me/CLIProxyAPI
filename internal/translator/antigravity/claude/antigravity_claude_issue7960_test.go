package claude

import (
	"context"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature/signaturetest"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestConvertClaudeRequestToAntigravityScopesRecoveredSignature(t *testing.T) {
	cache.ClearSignatureCache("")
	t.Cleanup(func() { cache.ClearSignatureCache("") })

	const model = "claude-sonnet-4-6"
	const thinking = "same thinking text"
	signature := signaturetest.AntigravityCAQSWithChannel(37)
	accountA := cache.WithSignatureCacheScope(context.Background(), "account-a\x00project-a")
	if !cache.CacheSignatureBestEffort(accountA, model, thinking, signature) {
		t.Fatal("failed to seed signature cache")
	}

	input := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"same thinking text"}]},{"role":"user","content":"continue"}]}`)
	translate := func(ctx context.Context) []byte {
		t.Helper()
		translated := sdktranslator.TranslateRequestEnvelope(ctx, sdktranslator.FormatClaude, sdktranslator.FormatAntigravity, sdktranslator.RequestEnvelope{
			Format: sdktranslator.FormatClaude,
			Model:  model,
			Body:   input,
		})
		if translated.Err != nil {
			t.Fatalf("TranslateRequestEnvelope() error = %v", translated.Err)
		}
		return translated.Body
	}

	accountB := cache.WithSignatureCacheScope(context.Background(), "account-b\x00project-b")
	for name, ctx := range map[string]context.Context{
		"different account": accountB,
		"unscoped legacy":   context.Background(),
	} {
		t.Run(name, func(t *testing.T) {
			body := translate(ctx)
			if got := ojson.GetBytes(body, "request.contents.0.parts.0.thoughtSignature").String(); got != "" {
				t.Fatalf("request recovered another credential signature %q; body=%s", got, body)
			}
		})
	}

	recovered := translate(accountA)
	if got := ojson.GetBytes(recovered, "request.contents.0.parts.0.thoughtSignature").String(); got == "" {
		t.Fatal("same credential did not recover its cached signature")
	}
}
