package session

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestDeriveIDStableAcrossConversationGrowth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		format sdktranslator.Format
		first  string
		later  string
	}{
		{
			name:   "openai chat",
			format: sdktranslator.FormatOpenAI,
			first:  `{"messages":[{"role":"system","content":"system prompt"},{"role":"developer","content":"developer prompt"},{"role":"user","content":"complete first user prompt"}]}`,
			later:  `{"messages":[{"role":"system","content":"system prompt"},{"role":"developer","content":"developer prompt"},{"role":"user","content":"complete first user prompt"},{"role":"assistant","content":"answer"},{"role":"developer","content":"later instruction"},{"role":"user","content":"next"}]}`,
		},
		{
			name:   "claude messages",
			format: sdktranslator.FormatClaude,
			first:  `{"system":[{"type":"text","text":"system prompt"}],"messages":[{"role":"user","content":[{"type":"text","text":"complete first user prompt"}]}]}`,
			later:  `{"system":[{"type":"text","text":"system prompt"}],"messages":[{"role":"user","content":[{"type":"text","text":"complete first user prompt"}]},{"role":"assistant","content":"answer"},{"role":"user","content":"next"}]}`,
		},
		{
			name:   "openai responses",
			format: sdktranslator.FormatOpenAIResponse,
			first:  `{"instructions":"system prompt","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"developer prompt"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"complete first user prompt"}]}]}`,
			later:  `{"instructions":"system prompt","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"developer prompt"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"complete first user prompt"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}]}`,
		},
		{
			name:   "gemini",
			format: sdktranslator.FormatGemini,
			first:  `{"systemInstruction":{"parts":[{"text":"system prompt"}]},"contents":[{"role":"user","parts":[{"text":"complete first user prompt"}]}]}`,
			later:  `{"systemInstruction":{"parts":[{"text":"system prompt"}]},"contents":[{"role":"user","parts":[{"text":"complete first user prompt"}]},{"role":"model","parts":[{"text":"answer"}]},{"role":"user","parts":[{"text":"next"}]}]}`,
		},
		{
			name:   "interactions",
			format: sdktranslator.FormatInteractions,
			first:  `{"system_instruction":"system prompt","input":[{"type":"developer_instruction","text":"developer prompt"},{"type":"user_input","content":[{"type":"text","text":"complete first user prompt"}]}]}`,
			later:  `{"system_instruction":"system prompt","input":[{"type":"developer_instruction","text":"developer prompt"},{"type":"user_input","content":[{"type":"text","text":"complete first user prompt"}]},{"type":"model_output","content":[{"type":"text","text":"answer"}]},{"type":"user_input","content":[{"type":"text","text":"next"}]}]}`,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			firstID := DeriveID(test.format, []byte(test.first), "caller-a")
			laterID := DeriveID(test.format, []byte(test.later), "caller-a")
			if firstID == "" {
				t.Fatal("DeriveID() returned empty")
			}
			if firstID != laterID {
				t.Fatalf("conversation growth changed identity: first=%q later=%q", firstID, laterID)
			}
		})
	}
}

func TestDeriveIDInstructionPrefixAndFullUser(t *testing.T) {
	t.Parallel()

	prefix := strings.Repeat("界", 50)
	first := []byte(`{"messages":[{"role":"system","content":"` + prefix + `timestamp-a"},{"role":"user","content":"` + strings.Repeat("u", 120) + `a"}]}`)
	sameRoot := []byte(`{"messages":[{"role":"system","content":"` + prefix + `timestamp-b"},{"role":"user","content":"` + strings.Repeat("u", 120) + `a"}]}`)
	differentUser := []byte(`{"messages":[{"role":"system","content":"` + prefix + `timestamp-b"},{"role":"user","content":"` + strings.Repeat("u", 120) + `b"}]}`)

	firstID := DeriveID(sdktranslator.FormatOpenAI, first, "caller-a")
	if firstID == "" {
		t.Fatal("DeriveID() returned empty")
	}
	if got := DeriveID(sdktranslator.FormatOpenAI, sameRoot, "caller-a"); got != firstID {
		t.Fatalf("content after 50 Unicode characters changed identity: got=%q want=%q", got, firstID)
	}
	if got := DeriveID(sdktranslator.FormatOpenAI, differentUser, "caller-a"); got == firstID {
		t.Fatal("different full first user prompt produced the same identity")
	}
}

func TestDeriveIDCallerIsolationAndGeminiCachedContent(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"messages":[{"role":"user","content":"same prompt"}]}`)
	callerA := DeriveID(sdktranslator.FormatOpenAI, payload, CallerScope("api-key-a"))
	callerB := DeriveID(sdktranslator.FormatOpenAI, payload, CallerScope("api-key-b"))
	if callerA == "" || callerB == "" || callerA == callerB {
		t.Fatalf("caller isolation failed: callerA=%q callerB=%q", callerA, callerB)
	}

	firstCached := []byte(`{"cachedContent":"cachedContents/abc","contents":[{"role":"user","parts":[{"text":"first"}]}]}`)
	grownCached := []byte(`{"cachedContent":"cachedContents/abc","contents":[{"role":"user","parts":[{"text":"first"}]},{"role":"model","parts":[{"text":"answer"}]},{"role":"user","parts":[{"text":"next"}]}]}`)
	differentCached := []byte(`{"cachedContent":"cachedContents/abc","contents":[{"role":"user","parts":[{"text":"different"}]}]}`)
	firstID := DeriveID(sdktranslator.FormatGemini, firstCached, "caller-a")
	grownID := DeriveID(sdktranslator.FormatGemini, grownCached, "caller-a")
	differentID := DeriveID(sdktranslator.FormatGemini, differentCached, "caller-a")
	if firstID == "" || firstID != grownID {
		t.Fatalf("cachedContent conversation growth changed identity: first=%q grown=%q", firstID, grownID)
	}
	if differentID == firstID {
		t.Fatalf("different first user prompts sharing cachedContent produced the same identity: %q", firstID)
	}
}

func TestDeriveIDRequiresFirstUser(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"messages":[{"role":"system","content":"shared system"}]}`)
	if got := DeriveID(sdktranslator.FormatOpenAI, payload, "caller-a"); got != "" {
		t.Fatalf("DeriveID() = %q, want empty without first user", got)
	}
}

func TestEnrichSkipsDerivationForExplicitSessions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		payload         []byte
		headers         http.Header
		requestMetadata map[string]any
		optionMetadata  map[string]any
	}{
		{
			name:    "session header avoids malformed body parsing",
			payload: []byte(`not-json`),
			headers: http.Header{"X-Session-ID": []string{"header-session"}},
		},
		{
			name:    "Claude Code session header",
			payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
			headers: http.Header{"X-Claude-Code-Session-Id": []string{"claude-session"}},
		},
		{
			name:    "later valid multi-value session header",
			payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
			headers: http.Header{"X-Session-Affinity": []string{"", "later-valid-session"}},
		},
		{
			name:    "OpenCode affinity header",
			payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
			headers: http.Header{"X-Session-Affinity": []string{"opencode-session"}},
		},
		{
			name:    "Responses conversation object",
			payload: []byte(`{"conversation":{"id":"conversation-session"},"messages":[{"role":"user","content":"hello"}]}`),
		},
		{
			name:    "Responses conversation string",
			payload: []byte(`{"conversation":"conversation-session","messages":[{"role":"user","content":"hello"}]}`),
		},
		{
			name:    "metadata user id",
			payload: []byte(`{"metadata":{"user_id":"explicit-user"},"messages":[{"role":"user","content":"hello"}]}`),
		},
		{
			name: "long legacy Claude metadata session",
			payload: []byte(`{"metadata":{"user_id":"` + strings.Repeat("x", 300) +
				`_session_ac980658-63bd-4fb3-97ba-8da64cb1e344"},"messages":[{"role":"user","content":"hello"}]}`),
		},
		{
			name:    "JSON metadata user id without nested session",
			payload: []byte(`{"metadata":{"user_id":"{\"device_id\":\"abc123\"}"},"messages":[{"role":"user","content":"hello"}]}`),
		},
		{
			name:    "body session id",
			payload: []byte(`{"session_id":"body-session","messages":[{"role":"user","content":"hello"}]}`),
		},
		{
			name:    "prompt cache key",
			payload: []byte(`{"prompt_cache_key":"cache-session","input":"hello"}`),
		},
		{
			name:           "execution session option metadata",
			payload:        []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
			optionMetadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "execution-session"},
		},
		{
			name:            "execution session request metadata",
			payload:         []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
			requestMetadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "execution-session"},
		},
		{
			name:    "explicit header removes stale derived identity",
			payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
			headers: http.Header{"x-session-id": []string{"header-session"}},
			optionMetadata: map[string]any{
				cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:stale",
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			req := cliproxyexecutor.Request{Payload: test.payload, Metadata: test.requestMetadata}
			opts := cliproxyexecutor.Options{
				OriginalRequest: test.payload,
				SourceFormat:    sdktranslator.FormatOpenAI,
				Headers:         test.headers,
				Metadata:        test.optionMetadata,
			}
			enrichedReq, enrichedOpts := Enrich(req, opts)
			if got := DerivedID(enrichedReq.Metadata); got != "" {
				t.Fatalf("request DerivedSessionID = %q, want empty", got)
			}
			if got := DerivedID(enrichedOpts.Metadata); got != "" {
				t.Fatalf("options DerivedSessionID = %q, want empty", got)
			}
			if test.name == "execution session option metadata" || test.name == "execution session request metadata" {
				if got := metadataString(enrichedReq.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); got != "execution-session" {
					t.Fatalf("request execution session = %q, want execution-session", got)
				}
				if got := metadataString(enrichedOpts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); got != "execution-session" {
					t.Fatalf("options execution session = %q, want execution-session", got)
				}
			}
		})
	}
}

func TestEnrichDerivesAfterInvalidSessionIdentity(t *testing.T) {
	t.Parallel()

	baseMessages := `"input":"hello"`
	tests := []struct {
		name            string
		payload         []byte
		headers         http.Header
		requestMetadata map[string]any
		optionMetadata  map[string]any
	}{
		{
			name:    "oversized prompt cache key",
			payload: []byte(`{"prompt_cache_key":"` + strings.Repeat("x", 257) + `",` + baseMessages + `}`),
		},
		{
			name:    "trailing control character prompt cache key",
			payload: []byte(`{"prompt_cache_key":"tenant\n",` + baseMessages + `}`),
		},
		{
			name:    "leading control character prompt cache key",
			payload: []byte(`{"prompt_cache_key":"\ttenant",` + baseMessages + `}`),
		},
		{
			name:    "control character session header",
			payload: []byte(`{` + baseMessages + `}`),
			headers: http.Header{"X-Session-Affinity": []string{"bad\nsession"}},
		},
		{
			name:           "oversized execution session option metadata",
			payload:        []byte(`{"input":"hello"}`),
			optionMetadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: strings.Repeat("x", 257)},
		},
		{
			name:            "control character execution session request metadata",
			payload:         []byte(`{"input":"hello"}`),
			requestMetadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "bad\nsession"},
		},
		{
			name:           "oversized retained derived session option metadata",
			payload:        []byte(`{"input":"hello"}`),
			optionMetadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: strings.Repeat("x", 257)},
		},
		{
			name:            "control character retained derived session request metadata",
			payload:         []byte(`{"input":"hello"}`),
			requestMetadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "bad\nsession"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			req := cliproxyexecutor.Request{Payload: test.payload, Metadata: test.requestMetadata}
			opts := cliproxyexecutor.Options{
				OriginalRequest: test.payload,
				SourceFormat:    sdktranslator.FormatOpenAIResponse,
				Headers:         test.headers,
				Metadata:        test.optionMetadata,
			}
			enrichedReq, enrichedOpts := Enrich(req, opts)
			requestID := DerivedID(enrichedReq.Metadata)
			optionsID := DerivedID(enrichedOpts.Metadata)
			wantID := DeriveID(sdktranslator.FormatOpenAIResponse, test.payload, "")
			if requestID != wantID || optionsID != wantID {
				t.Fatalf("derived identities = request:%q options:%q, want %q", requestID, optionsID, wantID)
			}
			if got := metadataString(enrichedReq.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); got != "" {
				t.Fatalf("request execution session = %q, want invalid value removed", got)
			}
			if got := metadataString(enrichedOpts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); got != "" {
				t.Fatalf("options execution session = %q, want invalid value removed", got)
			}
		})
	}
}

func TestEnrichCopiesDerivedIdentityToRequestAndOptions(t *testing.T) {
	t.Parallel()

	req := cliproxyexecutor.Request{Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}
	opts := cliproxyexecutor.Options{
		OriginalRequest: req.Payload,
		SourceFormat:    sdktranslator.FormatOpenAI,
		Metadata:        map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "caller-a"},
	}

	enrichedReq, enrichedOpts := Enrich(req, opts)
	reqID := DerivedID(enrichedReq.Metadata)
	optsID := DerivedID(enrichedOpts.Metadata)
	if reqID == "" || reqID != optsID {
		t.Fatalf("derived metadata mismatch: request=%q options=%q", reqID, optsID)
	}
	if _, exists := req.Metadata[cliproxyexecutor.DerivedSessionIDMetadataKey]; exists {
		t.Fatal("Enrich() mutated original request metadata")
	}
}

func TestEnrichCarriesRequestPayloadIntoSelectionOptions(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"conversation":{"id":"request-only-conversation"},"input":"hello"}`)
	_, enrichedOpts := Enrich(
		cliproxyexecutor.Request{Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse},
	)

	if !bytes.Equal(enrichedOpts.OriginalRequest, payload) {
		t.Fatalf("OriginalRequest = %q, want request payload %q", enrichedOpts.OriginalRequest, payload)
	}
	if len(enrichedOpts.OriginalRequest) > 0 && &enrichedOpts.OriginalRequest[0] == &payload[0] {
		t.Fatal("OriginalRequest aliases Request.Payload instead of preserving a snapshot")
	}
	if got := DerivedID(enrichedOpts.Metadata); got != "" {
		t.Fatalf("DerivedSessionID = %q, want explicit conversation to remain authoritative", got)
	}
}
