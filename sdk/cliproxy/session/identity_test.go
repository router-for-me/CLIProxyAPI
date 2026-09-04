package session

import (
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
		{
			name:    "nested request sessionId",
			payload: []byte(`{"request":{"sessionId":"nested-session"},"messages":[{"role":"user","content":"hello"}]}`),
		},
		{
			name:    "nested request subagent",
			payload: []byte(`{"request":{"sessionId":"nested-session","metadata":{"agent_id":"worker"}},"messages":[{"role":"user","content":"hello"}]}`),
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

func TestDeriveIDAntigravityNestedRequestAndEmptyFirstUser(t *testing.T) {
	t.Parallel()

	nestedAntigravity := []byte(`{
		"project_id": "test-project",
		"request": {
			"systemInstruction": {"parts":[{"text":"system prompt"}]},
			"contents": [
				{"role":"user","parts":[{"text":""}]},
				{"role":"user","parts":[{"text":"actual user prompt"}]}
			]
		}
	}`)
	id := DeriveID(sdktranslator.FormatAntigravity, nestedAntigravity, "caller-a")
	if id == "" {
		t.Fatal("DeriveID returned empty for nested Antigravity request with empty first turn")
	}

	directAntigravity := []byte(`{
		"systemInstruction": {"parts":[{"text":"system prompt"}]},
		"contents": [
			{"role":"user","parts":[{"text":"actual user prompt"}]}
		]
	}`)
	directID := DeriveID(sdktranslator.FormatAntigravity, directAntigravity, "caller-a")
	if id != directID {
		t.Fatalf("DeriveID mismatch: nested=%s, direct=%s", id, directID)
	}
}

func TestClaudeMetadataIdentitiesNormalizesAgentID(t *testing.T) {
	t.Parallel()

	validPayload := []byte(`{
		"metadata": {
			"user_id": "{\"session_id\":\"sess-123\",\"parent_session_id\":\"parent-456\",\"agent_id\":\"  subagent-alpha  \"}"
		}
	}`)
	sessionID, parentSessionID, agentID := ClaudeMetadataIdentities(validPayload)
	if sessionID != "sess-123" {
		t.Fatalf("sessionID = %q, want sess-123", sessionID)
	}
	if parentSessionID != "parent-456" {
		t.Fatalf("parentSessionID = %q, want parent-456", parentSessionID)
	}
	if agentID != "subagent-alpha" {
		t.Fatalf("agentID = %q, want subagent-alpha", agentID)
	}

	invalidPayload := []byte(`{
		"metadata": {
			"user_id": "{\"session_id\":\"sess-123\",\"agent_id\":\"bad\nagent\"}"
		}
	}`)
	_, _, badAgentID := ClaudeMetadataIdentities(invalidPayload)
	if badAgentID != "" {
		t.Fatalf("expected badAgentID to be empty for control character, got %q", badAgentID)
	}
}

func TestEnrich_ExplicitSessionPreferredOverExecutionSession(t *testing.T) {
	t.Parallel()

	// Scenario 1: Responses WebSocket provides ExecutionSessionMetadataKey for transport reuse,
	// but the client payload carries an explicit conversation/session identity.
	wsPayload := []byte(`{"session_id":"explicit-ws-session"}`)
	req1 := cliproxyexecutor.Request{
		Payload: wsPayload,
	}
	opts1 := cliproxyexecutor.Options{
		OriginalRequest: wsPayload,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "conn-ws-uuid-123",
		},
	}

	enrichedReq1, enrichedOpts1 := Enrich(req1, opts1)
	canonical1, ok1 := enrichedOpts1.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string)
	if !ok1 || canonical1 != "session:explicit-ws-session" {
		t.Fatalf("canonical session = %q (ok=%v), want session:explicit-ws-session", canonical1, ok1)
	}
	execID1, okExec1 := enrichedOpts1.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey].(string)
	if !okExec1 || execID1 != "conn-ws-uuid-123" {
		t.Fatalf("execution session was not preserved: %q (ok=%v)", execID1, okExec1)
	}
	_ = enrichedReq1

	// Scenario 2: Only ExecutionSessionMetadataKey exists (no explicit session headers or payload).
	req2 := cliproxyexecutor.Request{
		Payload: []byte(`{"model":"test"}`),
	}
	opts2 := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "conn-ws-uuid-456",
		},
	}
	_, enrichedOpts2 := Enrich(req2, opts2)
	canonical2, ok2 := enrichedOpts2.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string)
	if !ok2 || canonical2 != "execution:conn-ws-uuid-456" {
		t.Fatalf("canonical fallback = %q (ok=%v), want execution:conn-ws-uuid-456", canonical2, ok2)
	}
	execID2, okExec2 := enrichedOpts2.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey].(string)
	if !okExec2 || execID2 != "conn-ws-uuid-456" {
		t.Fatalf("execution session was not preserved in fallback: %q (ok=%v)", execID2, okExec2)
	}
}

func TestEnrich_MetadataOnlyCanonicalAndParentSessionPreserved(t *testing.T) {
	t.Parallel()

	// Embeddable SDK caller provides explicit canonical and parent session in Options.Metadata
	// without HTTP session headers or body session fields.
	req := cliproxyexecutor.Request{
		Payload: []byte(`{"model":"gpt-5.4"}`),
	}
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.CanonicalSessionIDMetadataKey: "session:sdk-child-001",
			cliproxyexecutor.ParentSessionIDMetadataKey:    "session:sdk-parent-999",
		},
	}

	_, enrichedOpts := Enrich(req, opts)
	canonical, ok := enrichedOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string)
	if !ok || canonical != "session:sdk-child-001" {
		t.Fatalf("canonical session = %q (ok=%v), want session:sdk-child-001", canonical, ok)
	}
	parent, okParent := enrichedOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey].(string)
	if !okParent || parent != "session:sdk-parent-999" {
		t.Fatalf("parent session = %q (ok=%v), want session:sdk-parent-999", parent, okParent)
	}

	// Self-loop elimination in metadata-only mode
	optsLoop := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.CanonicalSessionIDMetadataKey: "session:same-loop",
			cliproxyexecutor.ParentSessionIDMetadataKey:    "session:same-loop",
		},
	}
	_, enrichedLoop := Enrich(req, optsLoop)
	if _, hasLoopParent := enrichedLoop.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; hasLoopParent {
		t.Fatalf("self-loop parent was not eliminated in metadata-only mode")
	}
}
