package helps_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func TestRequestPairWithEmptyPluginHostSanitizesSignaturesOnce(t *testing.T) {
	logger := log.StandardLogger()
	oldOutput, oldLevel := logger.Out, logger.GetLevel()
	var logs bytes.Buffer
	logger.SetOutput(&logs)
	logger.SetLevel(log.DebugLevel)
	t.Cleanup(func() {
		logger.SetOutput(oldOutput)
		logger.SetLevel(oldLevel)
		sdktranslator.SetPluginHooks(nil)
	})

	var items []string
	for i := range 10 {
		items = append(items, fmt.Sprintf(`{"type":"function_call","call_id":"call_%d","name":"read_file","arguments":"{}"}`, i))
	}
	for i := range 10 {
		items = append(items, fmt.Sprintf(`{"type":"function_call_output","call_id":"call_%d","output":"ok"}`, i))
	}
	items = append(items, `{"role":"user","content":"continue"}`)
	payload := []byte(`{"model":"gemini-3.6-flash-high","input":[` + strings.Join(items, ",") + `]}`)
	inputBefore := bytes.Clone(payload)

	for _, host := range []sdktranslator.PluginHooks{nil, pluginhost.New()} {
		sdktranslator.SetPluginHooks(host)
		for _, stream := range []bool{false, true} {
			for _, route := range []string{"envelope", "api-key", "api-key-compat"} {
				logs.Reset()
				var base, work []byte
				if route == "envelope" {
					req := sdktranslator.RequestEnvelope{Format: sdktranslator.FormatOpenAIResponse, Model: "gemini-3.6-flash-high", Stream: stream}
					base, work = helps.TranslateRequestEnvelopePairWithCodexMultiAgentV2(t.Context(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatAntigravity, req, payload, payload)
				} else {
					base, work = helps.TranslateRequestPairWithAPIKeyModelCompatibility(t.Context(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatAntigravity, "gemini-3.6-flash-high", payload, payload, stream, route == "api-key-compat")
				}
				const message = "gemini request: sanitized 9 thoughtSignatures"
				if count := strings.Count(logs.String(), message); count != 1 {
					t.Fatalf("host=%T stream=%v route=%s: sanitation logs = %d, want 1; logs=%s", host, stream, route, count, logs.String())
				}
				if !bytes.Equal(base, work) {
					t.Fatal("same input produced different translations")
				}
				parts := gjson.GetBytes(work, "request.contents.0.parts").Array()
				if len(parts) != 10 || parts[0].Get("thoughtSignature").String() != "skip_thought_signature_validator" {
					t.Fatalf("unexpected parallel tool call layout: %s", work)
				}
				for _, part := range parts[1:] {
					if part.Get("thoughtSignature").Exists() {
						t.Fatal("sibling tool call still has a signature")
					}
				}
				baselineBefore := bytes.Clone(base)
				work[0] = 'X'
				if !bytes.Equal(base, baselineBefore) || !bytes.Equal(payload, inputBefore) {
					t.Fatal("working buffer aliases the baseline or input")
				}
			}
		}
	}
}
