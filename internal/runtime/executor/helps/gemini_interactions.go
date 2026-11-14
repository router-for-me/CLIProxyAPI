package helps

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const (
	// GeminiInteractionsAPIRevision is the Api-Revision header required for the Interactions API.
	GeminiInteractionsAPIRevision = "2026-05-20"
	// StreamScannerBuffer sets the scanner buffer capacity for streaming response chunks (50MB).
	StreamScannerBuffer = 52_428_800
)

// SanitizeGeminiInteractionsUnsupportedInputIDs aligns input step IDs with the
// official Gemini Interactions API schema:
// - `function_call` (FunctionCallStep) requires `id` and rejects `call_id`
// - `function_result` (FunctionResultStep) requires `call_id` and rejects `id`
// - other steps and content parts do not support `id`
//
// allowVertexBareTink must be true only for the Vertex AI Interactions endpoint;
// other endpoints reject Vertex bare-Tink thought signatures, so those thought
// steps are dropped like any other incompatible signature.
func SanitizeGeminiInteractionsUnsupportedInputIDs(body []byte, allowVertexBareTink bool) []byte {
	if len(body) == 0 {
		return body
	}
	doc := ojson.NewDoc(body)
	if !SanitizeGeminiInteractionsUnsupportedInputIDsDoc(doc, allowVertexBareTink) {
		return body
	}
	return doc.Bytes()
}

// SanitizeGeminiInteractionsUnsupportedInputIDsDoc aligns input step IDs with the
// official Gemini Interactions API schema in place on doc.
func SanitizeGeminiInteractionsUnsupportedInputIDsDoc(doc *ojson.Doc, allowVertexBareTink bool) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return false
	}
	items := input.Array()
	dropped := make(map[int]bool)
	changed := false
	for idx, item := range items {
		stepPath := fmt.Sprintf("input.%d", idx)
		stepType := item.Get("type").String()
		if stepType == "thought" {
			sig := strings.TrimSpace(item.Get("signature").String())
			if sig == "" {
				sig = strings.TrimSpace(item.Get("thought_signature").String())
			}
			if sig == "" {
				sig = strings.TrimSpace(item.Get("thoughtSignature").String())
			}
			normalizedSig, ok := signature.CompatibleSignatureForProviderBlock(
				signature.SignatureProviderGemini,
				sig,
				signature.SignatureBlockKindGeminiModelPart,
			)
			if !allowVertexBareTink && ok && signature.IsGeminiVertexBareTinkSignature(normalizedSig) {
				ok = false
			}
			if !ok || normalizedSig == "" || signature.IsGeminiThoughtSignatureBypass(signature.SignaturePayloadWithoutProviderPrefix(normalizedSig)) {
				// Official Gemini Interactions API rejects thought steps without a
				// valid Gemini signature (or with foreign/corrupt base64 signatures
				// or "content" arrays).
				dropped[idx] = true
				changed = true
				continue
			}
			if normalizedSig != item.Get("signature").String() {
				if errSet := doc.Set(stepPath+".signature", normalizedSig); errSet == nil {
					changed = true
				}
			}
			for _, unsupportedField := range []string{"id", "content", "text", "thought_signature", "thoughtSignature"} {
				if item.Get(unsupportedField).Exists() {
					if errDel := doc.Delete(stepPath + "." + unsupportedField); errDel == nil {
						changed = true
					}
				}
			}
			continue
		}
		if stepType == "function_call" {
			if !item.Get("id").Exists() && item.Get("call_id").Exists() {
				if errSet := doc.Set(stepPath+".id", item.Get("call_id").String()); errSet == nil {
					changed = true
				}
			}
			if item.Get("call_id").Exists() {
				if errDel := doc.Delete(stepPath + ".call_id"); errDel == nil {
					changed = true
				}
			}
		} else if item.Get("id").Exists() {
			if errDel := doc.Delete(stepPath + ".id"); errDel == nil {
				changed = true
			}
		}
		content := item.Get("content")
		if content.IsArray() {
			for j, part := range content.Array() {
				if part.Get("id").Exists() {
					if errDel := doc.Delete(fmt.Sprintf("%s.content.%d.id", stepPath, j)); errDel == nil {
						changed = true
					}
				}
			}
		}
	}
	if !changed {
		return false
	}
	if len(dropped) > 0 {
		updatedItems := doc.Get("input").Array()
		kept := make([][]byte, 0, len(updatedItems)-len(dropped))
		for idx, item := range updatedItems {
			if !dropped[idx] {
				kept = append(kept, []byte(item.Raw))
			}
		}
		return doc.SetRawBytes("input", JoinRawJSONArray(kept)) == nil
	}
	return true
}

// TranslateGeminiInteractionsRequestBody translates a request payload to FormatInteractions.
func TranslateGeminiInteractionsRequestBody(ctx context.Context, cfg *config.Config, model string, payload []byte, opts cliproxyexecutor.Options, stream, isCompat bool) ([]byte, error) {
	if opts.SourceFormat == "" || opts.SourceFormat == sdktranslator.FormatInteractions {
		return bytes.Clone(payload), nil
	}
	return TranslateRequestReturningError(ctx, opts.Headers, cfg, opts.SourceFormat, sdktranslator.FormatInteractions, model, payload, stream, isCompat)
}

// TranslateGeminiInteractionsRequestPair translates the working payload and the
// payload-config baseline. Identical inputs are translated once, including plugin
// hooks. The baseline is captured before model and thinking mutations, and the
// working buffer is a separate copy so those mutations cannot change it.
func TranslateGeminiInteractionsRequestPair(ctx context.Context, cfg *config.Config, model string, payload []byte, opts cliproxyexecutor.Options, stream, isCompat bool) (original, working []byte, err error) {
	source := geminiInteractionsPayloadConfigInput(opts, payload)
	if geminiInteractionsSameByteSlice(payload, source) {
		original, err = TranslateGeminiInteractionsRequestBody(ctx, cfg, model, payload, opts, stream, isCompat)
		return original, bytes.Clone(original), err
	}
	working, err = TranslateGeminiInteractionsRequestBody(ctx, cfg, model, payload, opts, stream, isCompat)
	original, _ = geminiInteractionsPayloadConfigSource(ctx, cfg, model, payload, opts, stream, isCompat)
	return original, working, err
}

func geminiInteractionsPayloadConfigSource(ctx context.Context, cfg *config.Config, model string, payload []byte, opts cliproxyexecutor.Options, stream, isCompat bool) ([]byte, error) {
	return TranslateGeminiInteractionsRequestBody(ctx, cfg, model, geminiInteractionsPayloadConfigInput(opts, payload), opts, stream, isCompat)
}

func geminiInteractionsPayloadConfigInput(opts cliproxyexecutor.Options, payload []byte) []byte {
	if len(opts.OriginalRequest) == 0 {
		return payload
	}
	return opts.OriginalRequest
}

func geminiInteractionsSameByteSlice(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	return &a[0] == &b[0]
}

// ApplyGeminiInteractionsThinking applies thinking/reasoning configuration to the interactions payload.
func ApplyGeminiInteractionsThinking(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) ([]byte, error) {
	fromFormat := opts.SourceFormat.String()
	if strings.TrimSpace(fromFormat) == "" {
		fromFormat = sdktranslator.FormatInteractions.String()
	}
	return ApplyRequestThinking(body, req, opts, fromFormat, sdktranslator.FormatInteractions.String(), "gemini")
}

// ApplyGeminiInteractionsThinkingDoc applies thinking/reasoning configuration to doc in place.
func ApplyGeminiInteractionsThinkingDoc(doc *ojson.Doc, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
	fromFormat := opts.SourceFormat.String()
	if strings.TrimSpace(fromFormat) == "" {
		fromFormat = sdktranslator.FormatInteractions.String()
	}
	return ApplyRequestThinkingDoc(doc, req, opts, fromFormat, sdktranslator.FormatInteractions.String(), "gemini")
}

// ApplyGeminiInteractionsRevisionHeader sets the default Api-Revision header on outgoing HTTP requests.
func ApplyGeminiInteractionsRevisionHeader(req *http.Request) {
	if req == nil {
		return
	}
	if req.Header.Get("Api-Revision") == "" {
		req.Header.Set("Api-Revision", GeminiInteractionsAPIRevision)
	}
}

// ApplyGeminiInteractionsRequestHeaders forwards any incoming Api-Revision header to upstream requests.
func ApplyGeminiInteractionsRequestHeaders(req *http.Request, headers http.Header) {
	if req == nil || headers == nil || req.Header.Get("Api-Revision") != "" {
		return
	}
	if revision := headers.Get("Api-Revision"); revision != "" {
		req.Header.Set("Api-Revision", revision)
	}
}

// GeminiInteractionsSSEPayload extracts SSE data payload from raw SSE frames.
func GeminiInteractionsSSEPayload(frame []byte) []byte {
	trimmed := bytes.TrimSpace(frame)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("{")) {
		return trimmed
	}
	lines := bytes.Split(frame, []byte{'\n'})
	var payload []byte
	for _, line := range lines {
		line = bytes.TrimRight(line, "\r")
		if !bytes.HasPrefix(bytes.TrimSpace(line), []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[bytes.Index(line, []byte("data:"))+len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if len(payload) > 0 {
			payload = append(payload, '\n')
		}
		payload = append(payload, data...)
	}
	if len(payload) == 0 {
		return nil
	}
	return payload
}

// GeminiInteractionsSSEDone reports whether the SSE frame signals stream completion.
func GeminiInteractionsSSEDone(frame []byte) bool {
	trimmed := bytes.TrimSpace(frame)
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return true
	}
	lines := bytes.Split(frame, []byte{'\n'})
	sawDoneEvent := false
	for _, line := range lines {
		line = bytes.TrimSpace(bytes.TrimRight(line, "\r"))
		if bytes.EqualFold(line, []byte("event: done")) {
			sawDoneEvent = true
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			data := bytes.TrimSpace(line[len("data:"):])
			if bytes.Equal(data, []byte("[DONE]")) {
				return true
			}
		}
	}
	return sawDoneEvent
}
