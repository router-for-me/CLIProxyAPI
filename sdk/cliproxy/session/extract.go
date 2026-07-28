package session

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// gatewaySessionHeader is the unified session header CPA asks controllable clients to send.
const gatewaySessionHeader = "X-Gateway-Session-Id"

// threadHeaders and parentThreadHeaders are observability signals. They describe a
// thread or sub-agent inside a conversation and never replace a root session.
//
// Claude Code sends X-Claude-Code-Agent-Id only for sub-agents: the main agent
// omits it entirely, so its presence identifies a sub-agent request. The parent
// header appears one level deeper still, because a depth-1 sub-agent's parent is
// the main agent, which has no agent identifier of its own.
var (
	threadHeaders       = []string{"X-Gateway-Thread-Id", "Thread-Id", "Thread_id", "X-Claude-Code-Agent-Id"}
	parentThreadHeaders = []string{"X-Gateway-Parent-Thread-Id", "X-Codex-Parent-Thread-Id", "X-Claude-Code-Parent-Agent-Id", "X-Parent-Session-Id"}
)

// Extract resolves one structured session identity from explicit client signals,
// then falls back to execution metadata, derived identity, and message history.
//
// All valid explicit root session signals in the same request are collected as
// aliases for the same logical session. The first signal collected becomes the
// representative Identity.ID, and all additional signals are placed in Identity.Aliases.
//
// When no explicit root session signal exists, Extract falls back to:
//  1. bare metadata.user_id (user scope fallback)
//  2. explicit execution session metadata
//  3. thread-id header (thread scope fallback)
//  4. stable context-derived session identity
//  5. stable hash from initial message content
func Extract(headers http.Header, payload []byte, metadata map[string]any) Identity {
	identity := extractRoot(headers, payload, metadata)
	identity.ClientType = DetectClientType(headers)
	identity.ThreadID = firstHeaderValue(headers, threadHeaders...)
	identity.ParentThreadID = firstHeaderValue(headers, parentThreadHeaders...)
	attachTurnObservations(&identity, headers)
	return identity
}

// codexTurnMetadataHeader carries Codex per-turn observability fields as a JSON
// object. It is never a session identifier: turn_id changes every turn, and the
// session and thread identifiers it repeats are already read from their own headers.
const codexTurnMetadataHeader = "X-Codex-Turn-Metadata"

// codexTurnMetadataMaxBytes bounds how much client-supplied JSON is parsed.
const codexTurnMetadataMaxBytes = 4096

// observationValueMaxLength bounds enum-like observation values so a client cannot
// push arbitrary length into logs and Home payloads.
const observationValueMaxLength = 64

// RequestKindBackground marks work a client runs outside the foreground conversation.
// Codex reports its own kinds verbatim (turn, compact, review, title, prewarm, ...);
// Claude Code only distinguishes background requests, via X-App.
const RequestKindBackground = "background"

// attachTurnObservations fills the observation-only fields describing what kind of
// request this is and where the thread came from. These never affect routing.
func attachTurnObservations(identity *Identity, headers http.Header) {
	if headers == nil {
		return
	}
	if strings.EqualFold(strings.TrimSpace(rawHeader(headers, "X-App")), "cli-bg") {
		identity.RequestKind = RequestKindBackground
	}

	raw := rawHeader(headers, codexTurnMetadataHeader)
	if raw == "" || len(raw) > codexTurnMetadataMaxBytes || !gjson.Valid(raw) {
		return
	}
	parsed := gjson.Parse(raw)
	if kind := normalizeObservationValue(parsed.Get("request_kind").String()); kind != "" {
		identity.RequestKind = kind
	}
	if source := normalizeObservationValue(parsed.Get("thread_source").String()); source != "" {
		identity.ThreadSource = source
	}
	if turnID := NormalizeExplicitID(parsed.Get("turn_id").String()); turnID != "" {
		identity.TurnID = turnID
	}
}

// normalizeObservationValue accepts a short enum-like token and rejects anything
// that could corrupt a log line or grow a payload.
func normalizeObservationValue(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > observationValueMaxLength {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return value
}

type rawSignal struct {
	id         string
	source     string
	confidence string
	scope      string
}

func extractRoot(headers http.Header, payload []byte, metadata map[string]any) Identity {
	signals := extractExplicitRootSignals(headers, payload)
	if len(signals) > 0 {
		seen := make(map[string]struct{}, len(signals))
		var deduplicated []rawSignal
		for _, s := range signals {
			if _, ok := seen[s.id]; !ok {
				seen[s.id] = struct{}{}
				deduplicated = append(deduplicated, s)
			}
		}

		if len(deduplicated) > 0 {
			rep := deduplicated[0]
			aliases := make([]string, 0, len(deduplicated)-1)
			for _, s := range deduplicated[1:] {
				aliases = append(aliases, s.id)
			}
			return Identity{
				ID:             rep.id,
				Aliases:        aliases,
				Source:         rep.source,
				Confidence:     rep.confidence,
				Scope:          rep.scope,
				ClientProvided: true,
			}
		}
	}

	if len(payload) > 0 {
		if userID := NormalizeExplicitID(gjson.GetBytes(payload, "metadata.user_id").String()); userID != "" {
			return clientIdentity("user:"+userID, SourceLegacyMetadataUser, ConfidenceLow, ScopeUser)
		}
	}

	if executionID, ok := metadata[cliproxyexecutor.ExecutionSessionMetadataKey].(string); ok {
		if executionID = NormalizeExplicitID(executionID); executionID != "" {
			return Identity{
				ID:         "execution:" + executionID,
				Source:     SourceExecutionSession,
				Confidence: ConfidenceHigh,
				Scope:      ScopeTransport,
			}
		}
	}
	if threadID := firstHeaderValue(headers, threadHeaders...); threadID != "" {
		return clientIdentity("thread:"+threadID, SourceClientNativeHeader, ConfidenceMedium, ScopeThread)
	}
	if derivedID := NormalizeExplicitID(DerivedID(metadata)); derivedID != "" {
		return Identity{
			ID:         "derived:" + derivedID,
			Source:     SourceDerived,
			Confidence: ConfidenceMedium,
			Scope:      ScopeSession,
		}
	}
	if len(payload) == 0 {
		return Identity{}
	}
	primary, fallback := extractMessageHashIDs(payload)
	if primary == "" {
		return Identity{}
	}
	return Identity{
		ID:         primary,
		Aliases:    nonEmptyAliases(fallback),
		Source:     SourceMessageHash,
		Confidence: ConfidenceLow,
		Scope:      ScopeSession,
	}
}

func extractExplicitRootSignals(headers http.Header, payload []byte) []rawSignal {
	signals := make([]rawSignal, 0, 13)
	appendHeader := func(name, prefix, source, confidence string) {
		if sid := HeaderValue(headers, name); sid != "" {
			signals = append(signals, rawSignal{prefix + sid, source, confidence, ScopeSession})
		}
	}

	appendHeader(gatewaySessionHeader, "gateway:", SourceGatewayHeader, ConfidenceHigh)
	appendHeader("X-Claude-Code-Session-Id", "claude:", SourceClientNativeHeader, ConfidenceHigh)
	if sid := ClaudeMetadataSessionID(payload); sid != "" {
		signals = append(signals, rawSignal{"claude:" + sid, SourceMetadataUserID, ConfidenceHigh, ScopeSession})
	}
	appendHeader("Session-Id", "codex:", SourceClientNativeHeader, ConfidenceHigh)
	appendHeader("Session_id", "codex:", SourceClientNativeHeader, ConfidenceHigh)
	appendHeader("X-Opencode-Session", "opencode:", SourceClientNativeHeader, ConfidenceHigh)
	appendHeader("X-Session-ID", "header:", SourceClientNativeHeader, ConfidenceHigh)
	appendHeader("X-Session-Affinity", "affinity:", SourceClientNativeHeader, ConfidenceHigh)
	// OpenCode gives a sub-agent its own X-Session-Id and puts the root session in
	// X-Parent-Session-Id, the opposite of the Claude Code and Codex convention.
	// Collecting the parent under the same "header:" prefix as X-Session-Id makes a
	// child request join the parent's alias group, so sub-agents reuse the parent's
	// credential instead of being routed as an unrelated session.
	appendHeader("X-Parent-Session-Id", "header:", SourceClientNativeHeader, ConfidenceHigh)

	if len(payload) == 0 {
		return appendClientRequestFallback(signals, headers)
	}
	for _, path := range []string{"session_id", "sessionId"} {
		if sid := NormalizeExplicitID(gjson.GetBytes(payload, path).String()); sid != "" {
			signals = append(signals, rawSignal{"session:" + sid, SourceBodySession, ConfidenceHigh, ScopeSession})
		}
	}
	if sid := NormalizeExplicitID(gjson.GetBytes(payload, "metadata.session_id").String()); sid != "" {
		signals = append(signals, rawSignal{"session:" + sid, SourceBodySession, ConfidenceMedium, ScopeSession})
	}
	if sid := NormalizeExplicitID(gjson.GetBytes(payload, "prompt_cache_key").String()); sid != "" {
		signals = append(signals, rawSignal{"pck:" + sid, SourcePromptCacheKey, ConfidenceMedium, ScopeSession})
	}
	conversation := gjson.GetBytes(payload, "conversation")
	if sid := NormalizeExplicitID(conversation.Get("id").String()); sid != "" {
		signals = append(signals, rawSignal{"conv:" + sid, SourceBodyConversation, ConfidenceHigh, ScopeSession})
	} else if conversation.Type == gjson.String {
		if sid := NormalizeExplicitID(conversation.String()); sid != "" {
			signals = append(signals, rawSignal{"conv:" + sid, SourceBodyConversation, ConfidenceHigh, ScopeSession})
		}
	}
	if sid := NormalizeExplicitID(gjson.GetBytes(payload, "conversation_id").String()); sid != "" {
		signals = append(signals, rawSignal{"conv:" + sid, SourceBodyConversation, ConfidenceHigh, ScopeSession})
	}
	return appendClientRequestFallback(signals, headers)
}

// appendClientRequestFallback uses X-Client-Request-Id only when the request
// carries no other explicit root identifier.
//
// The header tracks the current thread rather than the root session: a Codex
// sub-agent sends its own thread identifier there while session-id stays on the
// root. Collecting it unconditionally would add one throwaway alias per sub-agent
// to the root alias group. Every observed client that sends it also sends
// session-id or prompt_cache_key with the same value, so it is only reached by a
// client that sends nothing else, where it is the one signal available.
func appendClientRequestFallback(signals []rawSignal, headers http.Header) []rawSignal {
	if len(signals) > 0 {
		return signals
	}
	sid := HeaderValue(headers, "X-Client-Request-Id")
	if sid == "" {
		return signals
	}
	return append(signals, rawSignal{"clientreq:" + sid, SourceClientRequestID, ConfidenceMedium, ScopeSession})
}

func clientIdentity(id, source, confidence, scope string) Identity {
	return Identity{
		ID:             id,
		Source:         source,
		Confidence:     confidence,
		Scope:          scope,
		ClientProvided: true,
	}
}

func nonEmptyAliases(aliases ...string) []string {
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		if alias != "" {
			out = append(out, alias)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// HeaderValue returns the first valid explicit identifier stored under name.
// Header names are compared case-insensitively and invalid values are skipped.
func HeaderValue(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if value := NormalizeExplicitID(headers.Get(name)); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, raw := range values {
			if value := NormalizeExplicitID(raw); value != "" {
				return value
			}
		}
	}
	return ""
}

func firstHeaderValue(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := HeaderValue(headers, name); value != "" {
			return value
		}
	}
	return ""
}

func extractMessageHashIDs(payload []byte) (primaryID, fallbackID string) {
	var systemPrompt, firstUserMsg, firstAssistantMsg string

	// OpenAI/Claude messages format
	messages := gjson.GetBytes(payload, "messages")
	if messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			role := msg.Get("role").String()
			content := extractMessageContent(msg.Get("content"))
			if content == "" {
				return true
			}

			switch role {
			case "system":
				if systemPrompt == "" {
					systemPrompt = truncateString(content, 100)
				}
			case "user":
				if firstUserMsg == "" {
					firstUserMsg = truncateString(content, 100)
				}
			case "assistant":
				if firstAssistantMsg == "" {
					firstAssistantMsg = truncateString(content, 100)
				}
			}

			if systemPrompt != "" && firstUserMsg != "" && firstAssistantMsg != "" {
				return false
			}
			return true
		})
	}

	// Claude API: top-level "system" field (array or string)
	if systemPrompt == "" {
		topSystem := gjson.GetBytes(payload, "system")
		if topSystem.Exists() {
			if topSystem.IsArray() {
				topSystem.ForEach(func(_, part gjson.Result) bool {
					if text := part.Get("text").String(); text != "" && systemPrompt == "" {
						systemPrompt = truncateString(text, 100)
						return false
					}
					return true
				})
			} else if topSystem.Type == gjson.String {
				systemPrompt = truncateString(topSystem.String(), 100)
			}
		}
	}

	// Gemini format
	if systemPrompt == "" && firstUserMsg == "" {
		sysInstr := gjson.GetBytes(payload, "systemInstruction.parts")
		if sysInstr.Exists() && sysInstr.IsArray() {
			sysInstr.ForEach(func(_, part gjson.Result) bool {
				if text := part.Get("text").String(); text != "" && systemPrompt == "" {
					systemPrompt = truncateString(text, 100)
					return false
				}
				return true
			})
		}

		contents := gjson.GetBytes(payload, "contents")
		if contents.Exists() && contents.IsArray() {
			contents.ForEach(func(_, msg gjson.Result) bool {
				role := msg.Get("role").String()
				msg.Get("parts").ForEach(func(_, part gjson.Result) bool {
					text := part.Get("text").String()
					if text == "" {
						return true
					}
					switch role {
					case "user":
						if firstUserMsg == "" {
							firstUserMsg = truncateString(text, 100)
						}
					case "model":
						if firstAssistantMsg == "" {
							firstAssistantMsg = truncateString(text, 100)
						}
					}
					return false
				})
				if firstUserMsg != "" && firstAssistantMsg != "" {
					return false
				}
				return true
			})
		}
	}

	// OpenAI Responses API format (v1/responses)
	if systemPrompt == "" && firstUserMsg == "" {
		if instr := gjson.GetBytes(payload, "instructions").String(); instr != "" {
			systemPrompt = truncateString(instr, 100)
		}

		input := gjson.GetBytes(payload, "input")
		if input.Exists() && input.IsArray() {
			input.ForEach(func(_, item gjson.Result) bool {
				itemType := item.Get("type").String()
				if itemType == "reasoning" {
					return true
				}
				// Skip non-message typed items (function_call, function_call_output, etc.)
				// but allow items with no type that have a role (inline message format).
				if itemType != "" && itemType != "message" {
					return true
				}

				role := item.Get("role").String()
				if itemType == "" && role == "" {
					return true
				}

				// Handle both string content and array content (multimodal).
				content := item.Get("content")
				var text string
				if content.Type == gjson.String {
					text = content.String()
				} else {
					text = extractResponsesAPIContent(content)
				}
				if text == "" {
					return true
				}

				switch role {
				case "developer", "system":
					if systemPrompt == "" {
						systemPrompt = truncateString(text, 100)
					}
				case "user":
					if firstUserMsg == "" {
						firstUserMsg = truncateString(text, 100)
					}
				case "assistant":
					if firstAssistantMsg == "" {
						firstAssistantMsg = truncateString(text, 100)
					}
				}

				if firstUserMsg != "" && firstAssistantMsg != "" {
					return false
				}
				return true
			})
		}
	}

	if systemPrompt == "" && firstUserMsg == "" {
		return "", ""
	}

	shortHash := computeSessionHash(systemPrompt, firstUserMsg, "")
	if firstAssistantMsg == "" {
		return shortHash, ""
	}

	fullHash := computeSessionHash(systemPrompt, firstUserMsg, firstAssistantMsg)
	return fullHash, shortHash
}

func computeSessionHash(systemPrompt, userMsg, assistantMsg string) string {
	h := fnv.New64a()
	if systemPrompt != "" {
		h.Write([]byte("sys:" + systemPrompt + "\n"))
	}
	if userMsg != "" {
		h.Write([]byte("usr:" + userMsg + "\n"))
	}
	if assistantMsg != "" {
		h.Write([]byte("ast:" + assistantMsg + "\n"))
	}
	return fmt.Sprintf("msg:%016x", h.Sum64())
}

func truncateString(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

// extractMessageContent extracts text content from a message content field.
// Handles both string content and array content (multimodal messages).
// For array content, extracts text from all text-type elements.
func extractMessageContent(content gjson.Result) string {
	// String content: "Hello world"
	if content.Type == gjson.String {
		return content.String()
	}

	// Array content: [{"type":"text","text":"Hello"},{"type":"image",...}]
	if content.IsArray() {
		var texts []string
		content.ForEach(func(_, part gjson.Result) bool {
			// Claude and OpenAI share the {"type":"text","text":"content"} shape.
			if part.Get("type").String() == "text" {
				if text := part.Get("text").String(); text != "" {
					texts = append(texts, text)
				}
			}
			return true
		})
		if len(texts) > 0 {
			return strings.Join(texts, " ")
		}
	}

	return ""
}

func extractResponsesAPIContent(content gjson.Result) string {
	if !content.IsArray() {
		return ""
	}
	var texts []string
	content.ForEach(func(_, part gjson.Result) bool {
		partType := part.Get("type").String()
		if partType == "input_text" || partType == "output_text" || partType == "text" {
			if text := part.Get("text").String(); text != "" {
				texts = append(texts, text)
			}
		}
		return true
	})
	if len(texts) > 0 {
		return strings.Join(texts, " ")
	}
	return ""
}
