package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestResponseNormalizerRequestBodyOptOut(t *testing.T) {
	for _, omit := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, after := range []bool{false, true} {
				t.Run(fmt.Sprintf("omit=%t/stream=%t/after=%t", omit, stream, after), func(t *testing.T) {
					original := []byte(`{"model":"factory/claude-opus-5-5","input":"private history"}`)
					translated := []byte(`{"model":"claude-opus-5-5","messages":[]}`)
					body := []byte("fragment")
					calls := 0
					normalizer := responseNormalizerFunc(func(_ context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
						calls++
						if req.Stream != stream || req.Model != "claude-opus-5-5" || !bytes.Equal(req.Body, body) {
							t.Fatalf("response metadata changed: %#v", req)
						}
						if omit {
							if req.OriginalRequest != nil || req.TranslatedRequest != nil || req.RequestedModel != "factory/claude-opus-5-5" {
								t.Fatalf("opt-out retained history or lost requested model: %#v", req)
							}
						} else {
							if !bytes.Equal(req.OriginalRequest, original) || !bytes.Equal(req.TranslatedRequest, translated) || req.RequestedModel != "" {
								t.Fatal("legacy payload changed")
							}
							req.OriginalRequest[0] = '!'
							req.TranslatedRequest[0] = '!'
						}
						return pluginapi.PayloadResponse{Body: []byte("normalized")}, nil
					})
					caps := pluginapi.Capabilities{ResponseNormalizerOmitRequestBody: omit, ResponseBeforeTranslator: normalizer, ResponseAfterTranslator: normalizer}
					host := newHostWithRecords(capabilityRecord{id: "normalizer", plugin: pluginapi.Plugin{Capabilities: caps}})
					normalize := host.NormalizeResponseBefore
					if after {
						normalize = host.NormalizeResponseAfter
					}
					got := normalize(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "claude-opus-5-5", original, translated, body, stream)
					if calls != 1 || string(got) != "normalized" || original[0] != '{' || translated[0] != '{' {
						t.Fatal("normalization or request isolation changed")
					}
				})
			}
		}
	}
}

func TestResponseNormalizerRequestedModelFallback(t *testing.T) {
	for _, original := range []string{"", `{}`, `{"model":42}`, `{"model":""}`} {
		normalizer := responseNormalizerFunc(func(_ context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
			if req.RequestedModel != "gpt-native" {
				t.Fatalf("fallback model = %q", req.RequestedModel)
			}
			return pluginapi.PayloadResponse{Body: req.Body}, nil
		})
		host := newHostWithRecords(capabilityRecord{id: "normalizer", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ResponseNormalizerOmitRequestBody: true, ResponseAfterTranslator: normalizer,
		}}})
		host.NormalizeResponseAfter(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAI, "gpt-native", []byte(original), nil, []byte("fragment"), true)
	}
}

func TestResponseNormalizerRequestBodyOptOutRegistration(t *testing.T) {
	for _, omit := range []bool{false, true} {
		caps := rpcCapabilitiesFromPlugin(pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ResponseNormalizerOmitRequestBody: omit}})
		raw, err := json.Marshal(map[string]any{"ok": true, "result": rpcRegistration{SchemaVersion: 6, Capabilities: caps}})
		if err != nil {
			t.Fatal(err)
		}
		plugin, err := registerRPCPlugin(context.Background(), New(), "normalizer", staticEnvelopePluginClient{raw: raw}, "plugin.register", nil)
		if err != nil {
			t.Fatal(err)
		}
		if plugin.Capabilities.ResponseNormalizerOmitRequestBody != omit {
			t.Fatal("RPC registration lost opt-out")
		}
	}
}

// Include the ABI JSON encoding so this catches conversation-sized copies and
// base64 serialization that a no-op normalizer benchmark would miss.
func BenchmarkResponseNormalizerPayload(b *testing.B) {
	for _, omit := range []bool{false, true} {
		b.Run(fmt.Sprintf("omit=%t", omit), func(b *testing.B) {
			original := []byte(`{"model":"gpt-native","input":"` + strings.Repeat("x", 1024*1024) + `"}`)
			normalizer := responseNormalizerFunc(func(_ context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
				if _, err := json.Marshal(req); err != nil {
					b.Fatal(err)
				}
				return pluginapi.PayloadResponse{Body: req.Body}, nil
			})
			host := newHostWithRecords(capabilityRecord{id: "normalizer", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ResponseNormalizerOmitRequestBody: omit, ResponseAfterTranslator: normalizer,
			}}})
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				host.NormalizeResponseAfter(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAI, "gpt-native", original, original, []byte("fragment"), true)
			}
		})
	}
}
