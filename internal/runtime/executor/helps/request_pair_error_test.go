package helps

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	pairFileOnlyClaude = `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":[{"type":"container_upload","file_id":"file-1"}]}]}`
	pairTextOnlyClaude = `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":[{"type":"text","text":"keep me"}]}]}`
)

func requireUnsupportedPart(t *testing.T, label string, err error, wantType string) {
	t.Helper()
	var part *translatorcommon.UnsupportedPartError
	if !errors.As(err, &part) || part.Type != wantType || clienterror.HTTPStatusFromError(err) != http.StatusBadRequest {
		t.Fatalf("%s: err = %v, want a 400 unsupported content part: %s", label, err, wantType)
	}
	if got, want := err.Error(), "unsupported content part: "+wantType; got != want {
		t.Fatalf("%s: error text = %q, want %q", label, got, want)
	}
}

// Only the working payload reaches the upstream, so only its translation error
// may reject the request. The baseline serves payload rules and is never sent.
func TestPairTranslationReportsOnlyTheWorkingError(t *testing.T) {
	file := []byte(pairFileOnlyClaude)
	text := []byte(pairTextOnlyClaude)
	ctx := context.Background()

	type pairFunc func(original, working []byte) (originalBody, workingBody []byte, err error)
	routes := map[string]pairFunc{}
	routes["antigravity envelope pair"] = func(original, working []byte) ([]byte, []byte, error) {
		req := sdktranslator.RequestEnvelope{Format: sdktranslator.FormatClaude, Model: "m"}
		return TranslateRequestEnvelopePairWithCodexMultiAgentV2(ctx, nil, &config.Config{}, sdktranslator.FormatClaude, sdktranslator.FormatAntigravity, req, original, working)
	}
	for _, to := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatGemini, sdktranslator.FormatCodex, sdktranslator.FormatInteractions} {
		for _, compat := range []bool{false, true} {
			name := "api-key pair to " + to.String()
			if compat {
				name += " compat"
			}
			routes[name] = func(original, working []byte) ([]byte, []byte, error) {
				return TranslateRequestPairReturningError(ctx, nil, &config.Config{}, sdktranslator.FormatClaude, to, "m", original, working, false, compat)
			}
			routes[name+" with update intent"] = func(original, working []byte) ([]byte, []byte, error) {
				originalBody, workingBody, _, err := TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, nil, &config.Config{}, sdktranslator.FormatClaude, to, "m", original, working, false, compat)
				return originalBody, workingBody, err
			}
		}
	}

	for name, pair := range routes {
		t.Run(name, func(t *testing.T) {
			if _, working, err := pair(file, text); err != nil || !gjson.ValidBytes(working) {
				t.Fatalf("failing baseline rejected a working payload that translated: err = %v, working = %s", err, working)
			}
			if _, _, err := pair(text, file); err == nil {
				t.Fatal("a failing working payload was accepted")
			} else {
				requireUnsupportedPart(t, "working fails", err, "container_upload")
			}
			if _, _, err := pair(file, file); err == nil {
				t.Fatal("a failing payload shared by both sides was accepted")
			} else {
				requireUnsupportedPart(t, "same slice fails", err, "container_upload")
			}
			if _, _, err := pair(text, text); err != nil {
				t.Fatalf("same slice that translated returned %v", err)
			}
		})
	}
}

// A refused baseline never rejects or replaces the working payload that translated.
func TestEnvelopePairKeepsTheWorkingPayloadWhenTheBaselineIsRefused(t *testing.T) {
	file := []byte(pairFileOnlyClaude)
	text := []byte(pairTextOnlyClaude)
	req := sdktranslator.RequestEnvelope{Format: sdktranslator.FormatClaude, Model: "m"}
	_, working, err := TranslateRequestEnvelopePairWithCodexMultiAgentV2(context.Background(), nil, &config.Config{}, sdktranslator.FormatClaude, sdktranslator.FormatAntigravity, req, append([]byte(nil), file...), text)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := gjson.GetBytes(working, "request.contents.0.parts.0.text").String(); got != "keep me" {
		t.Fatalf("working = %s", working)
	}
}

// A Responses request whose only user content is an input_file file id must be
// refused with a named 400 on the default envelope path and on the compat path.
func TestResponsesFileIDOnlyReturns400OnEveryPath(t *testing.T) {
	payload := []byte(`{"model":"claude-sonnet-4","input":[{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file-1"}]}]}`)
	for _, compat := range []bool{false, true} {
		_, single := TranslateRequestReturningError(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4", payload, false, compat)
		requireUnsupportedPart(t, "single", single, "input_file")
		_, _, pair := TranslateRequestPairReturningError(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4", payload, payload, false, compat)
		requireUnsupportedPart(t, "pair", pair, "input_file")
		_, _, _, intent := TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4", payload, payload, false, compat)
		requireUnsupportedPart(t, "pair with update intent", intent, "input_file")
	}
}

func TestResponsesAudioAndTextBesideFileIDOnCompatPath(t *testing.T) {
	audio := []byte(`{"model":"claude-sonnet-4","input":[{"type":"message","role":"user","content":[{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}]}]}`)
	_, err := TranslateRequestReturningError(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4", audio, false, true)
	requireUnsupportedPart(t, "audio", err, "input_audio")

	mixed := []byte(`{"model":"claude-sonnet-4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"keep me"},{"type":"input_file","file_id":"file-1"}]}]}`)
	body, err := TranslateRequestReturningError(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4", mixed, false, true)
	if err != nil {
		t.Fatalf("text beside a file id was refused: %v", err)
	}
	if got := gjson.GetBytes(body, "messages.0.content").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", body)
	}
}

// Antigravity is reached through the same checked registry as every other target.
func TestAntigravityTargetsReportUnsupportedParts(t *testing.T) {
	claudeFile := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"container_upload","file_id":"file-1"}]}]}`)
	_, err := TranslateRequestReturningError(context.Background(), nil, &config.Config{}, sdktranslator.FormatClaude, sdktranslator.FormatAntigravity, "m", claudeFile, false, false)
	requireUnsupportedPart(t, "claude to antigravity", err, "container_upload")

	openAIFile := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"file","file":{"file_id":"file-1"}}]}]}`)
	_, err = TranslateRequestReturningError(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAI, sdktranslator.FormatAntigravity, "m", openAIFile, false, false)
	requireUnsupportedPart(t, "openai to antigravity", err, "file")
}
