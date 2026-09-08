// Package session derives stable conversation identities from protocol request roots.
package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	identityVersion      = "cpa-session-root-v1"
	identityPrefix       = "ctx:v1:"
	instructionRuneLimit = 50
)

var legacyClaudeSessionPattern = regexp.MustCompile(`_session_([a-f0-9-]+)$`)

type canonicalRoot struct {
	Version      string          `json:"version"`
	Format       string          `json:"format"`
	CallerScope  string          `json:"caller_scope"`
	Instructions []string        `json:"instructions,omitempty"`
	User         []canonicalPart `json:"user,omitempty"`
	Resource     string          `json:"resource,omitempty"`
}

type canonicalPart struct {
	Kind  string `json:"kind"`
	MIME  string `json:"mime,omitempty"`
	Value string `json:"value"`
}

// NormalizeExplicitID validates an explicit client-provided session identifier.
// It preserves opaque printable values while rejecting oversized or control-bearing IDs.
func NormalizeExplicitID(raw string) string {
	for _, r := range raw {
		if unicode.IsControl(r) {
			return ""
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 256 {
		return ""
	}
	return raw
}

// ClaudeMetadataIdentities extracts session_id, parent_session_id, and agent_id from Claude user_id metadata.
func ClaudeMetadataIdentities(payload []byte) (sessionID, parentSessionID, agentID string) {
	if len(payload) == 0 {
		return "", "", ""
	}
	root := util.ParseGJSONBytesNoCopy(payload)
	userID := strings.TrimSpace(root.Get("metadata.user_id").String())
	if userID == "" {
		req := root.Get("request")
		if req.Exists() && !root.Get("contents").Exists() {
			userID = strings.TrimSpace(req.Get("metadata.user_id").String())
		}
	}
	if userID == "" {
		return "", "", ""
	}
	if strings.HasPrefix(userID, "{") {
		parsed := gjson.Parse(userID)
		sessionID = NormalizeExplicitID(parsed.Get("session_id").String())
		parentSessionID = NormalizeExplicitID(parsed.Get("parent_session_id").String())
		agentID = NormalizeExplicitID(parsed.Get("agent_id").String())
		return sessionID, parentSessionID, agentID
	}
	if matches := legacyClaudeSessionPattern.FindStringSubmatch(userID); len(matches) >= 2 {
		return NormalizeExplicitID(matches[1]), "", ""
	}
	return "", "", ""
}

// ClaudeMetadataSessionID extracts the explicit Claude Code session from
// current JSON metadata or the legacy user_id suffix before bounding the
// surrounding metadata container.
func ClaudeMetadataSessionID(payload []byte) string {
	sessionID, _, _ := ClaudeMetadataIdentities(payload)
	return sessionID
}

// ClaudeMetadataParentSessionID extracts parent_session_id from Claude user_id metadata if present.
func ClaudeMetadataParentSessionID(payload []byte) string {
	_, parentSessionID, _ := ClaudeMetadataIdentities(payload)
	return parentSessionID
}

// CallerScope returns an irreversible namespace for a downstream caller credential.
func CallerScope(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("cli-proxy-api:caller-scope:v1\x00" + value))
	return hex.EncodeToString(sum[:])
}

// DerivedID returns a derived session identity stored in execution metadata.
func DerivedID(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[cliproxyexecutor.DerivedSessionIDMetadataKey].(string)
	return strings.TrimSpace(value)
}

// Enrich derives a session identity once and places it in both request and option metadata.
func Enrich(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	payload := opts.OriginalRequest
	if len(payload) == 0 && len(req.Payload) > 0 {
		opts.OriginalRequest = bytes.Clone(req.Payload)
		payload = opts.OriginalRequest
	}
	executionID := firstNormalizedMetadataID(cliproxyexecutor.ExecutionSessionMetadataKey, opts.Metadata, req.Metadata)

	if hasExplicitSession(opts.Headers, payload) {
		req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
		opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
		if executionID != "" {
			req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey, executionID)
			opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey, executionID)
		} else {
			req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
			opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
		}
		if info, ok := ExtractSessionInfo(opts.Headers, payload, opts.Metadata); ok && info.SessionID != "" {
			canonicalSessionID := BoundSessionIdentity(info.SessionID)
			req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey, canonicalSessionID)
			opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey, canonicalSessionID)
			if info.ParentSessionID != "" && info.ParentSessionID != info.SessionID {
				parentSessionID := BoundSessionIdentity(info.ParentSessionID)
				req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey, parentSessionID)
				opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey, parentSessionID)
			} else {
				req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
				opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
			}
		}
		return req, opts
	}

	// Metadata-provided canonical or LCP session (e.g. embeddable SDK callers or pre-routed stages)
	if canonicalID := firstNormalizedMetadataID(cliproxyexecutor.CanonicalSessionIDMetadataKey, opts.Metadata, req.Metadata); canonicalID != "" {
		req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
		opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
		if executionID != "" {
			req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey, executionID)
			opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey, executionID)
		} else {
			req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
			opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
		}
		canonicalSessionID := BoundSessionIdentity(canonicalID)
		req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey, canonicalSessionID)
		opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey, canonicalSessionID)
		if parentID := firstNormalizedMetadataID(cliproxyexecutor.ParentSessionIDMetadataKey, opts.Metadata, req.Metadata); parentID != "" && parentID != canonicalID {
			boundedParent := BoundSessionIdentity(parentID)
			req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey, boundedParent)
			opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey, boundedParent)
		} else {
			req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
			opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
		}
		return req, opts
	}

	if lcpID := firstNormalizedMetadataID(cliproxyexecutor.LCPAffinitySessionIDMetadataKey, opts.Metadata, req.Metadata); lcpID != "" {
		req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
		opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
		if executionID != "" {
			req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey, executionID)
			opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey, executionID)
		} else {
			req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
			opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
		}
		canonicalLCP := BoundSessionIdentity(lcpID)
		req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey, canonicalLCP)
		opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey, canonicalLCP)
		if parentID := firstNormalizedMetadataID(cliproxyexecutor.ParentSessionIDMetadataKey, opts.Metadata, req.Metadata); parentID != "" && parentID != lcpID {
			boundedParent := BoundSessionIdentity(parentID)
			req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey, boundedParent)
			opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey, boundedParent)
		} else {
			req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
			opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
		}
		return req, opts
	}

	if executionID != "" {
		canonicalExecutionID := BoundSessionIdentity("execution:" + executionID)
		req.Metadata = metadataWithValue(metadataWithValue(metadataWithoutKey(metadataWithoutKey(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey), cliproxyexecutor.ParentSessionIDMetadataKey), cliproxyexecutor.ExecutionSessionMetadataKey, executionID), cliproxyexecutor.CanonicalSessionIDMetadataKey, canonicalExecutionID)
		opts.Metadata = metadataWithValue(metadataWithValue(metadataWithoutKey(metadataWithoutKey(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey), cliproxyexecutor.ParentSessionIDMetadataKey), cliproxyexecutor.ExecutionSessionMetadataKey, executionID), cliproxyexecutor.CanonicalSessionIDMetadataKey, canonicalExecutionID)
		return req, opts
	}

	req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
	opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
	req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
	opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)

	derivedID := firstNormalizedMetadataID(cliproxyexecutor.DerivedSessionIDMetadataKey, opts.Metadata, req.Metadata)
	req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
	opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
	if derivedID == "" {
		callerScope := metadataString(opts.Metadata, cliproxyexecutor.CallerScopeMetadataKey)
		if callerScope == "" {
			callerScope = metadataString(req.Metadata, cliproxyexecutor.CallerScopeMetadataKey)
		}
		derivedID = DeriveID(opts.SourceFormat, payload, callerScope)
	}
	if derivedID == "" {
		return req, opts
	}
	req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey, derivedID)
	opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey, derivedID)
	return req, opts
}

func hasExplicitSession(headers map[string][]string, payload []byte) bool {
	for _, header := range []string{
		"X-Claude-Code-Session-Id",
		"X-Claude-Code-Agent-Id",
		"X-Claude-Code-Parent-Agent-Id",
		"Session-Id",
		"Session_id",
		"x-codex-parent-thread-id",
		"X-Codex-Parent-Thread-Id",
		"X-Http-Session-Id",
		"X-Session-ID",
		"X-Session-Affinity",
		"X-Parent-Session-ID",
		"X-Parent-Session-Id",
		"X-Parent-Session-Affinity",
		"X-Slot-Session-Id",
		"X-Conversation-Id",
		"X-Conversation-ID",
		"X-Thread-Id",
		"X-Thread-ID",
		"Thread-Id",
		"X-Client-Request-Id",
	} {
		if NormalizeExplicitID(headerValue(headers, header)) != "" {
			return true
		}
	}
	if len(payload) == 0 {
		return false
	}
	// Parsing without copying matters here: this runs on every request and the
	// payload can be multiple megabytes.
	root := util.ParseGJSONBytesNoCopy(payload)
	reqRoot := root
	req := root.Get("request")
	hasNestedReq := req.Exists() && !root.Get("contents").Exists()
	if hasNestedReq {
		reqRoot = req
	}
	for _, path := range []string{
		"session_id",
		"sessionId",
		"sessionID",
		"cachedContent",
		"cached_content",
		"thread_id",
		"threadId",
		"conversation_id",
		"conversationId",
		"chat_id",
		"chatId",
		"prompt_cache_key",
		"promptCacheKey",
		"parent_session_id",
		"parentSessionId",
		"parent_thread_id",
		"parentThreadId",
		"forked_from_thread_id",
		"forked_from_id",
		"metadata.session_id",
		"metadata.thread_id",
		"metadata.conversation_id",
		"extra_body.session_id",
	} {
		if NormalizeExplicitID(root.Get(path).String()) != "" {
			return true
		}
		if hasNestedReq && NormalizeExplicitID(reqRoot.Get(path).String()) != "" {
			return true
		}
	}
	if ClaudeMetadataSessionID(payload) != "" {
		return true
	}
	userID := strings.TrimSpace(root.Get("metadata.user_id").String())
	if userID == "" && hasNestedReq {
		userID = strings.TrimSpace(reqRoot.Get("metadata.user_id").String())
	}
	if NormalizeExplicitID(userID) != "" {
		return true
	}
	conversation := root.Get("conversation")
	if !conversation.Exists() && hasNestedReq {
		conversation = reqRoot.Get("conversation")
	}
	if NormalizeExplicitID(conversation.Get("id").String()) != "" {
		return true
	}
	return conversation.Type == gjson.String && NormalizeExplicitID(conversation.String()) != ""
}

func headerValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if normalized := NormalizeExplicitID(value); normalized != "" {
				return normalized
			}
		}
	}
	return ""
}

// DeriveID builds a stable identity from leading instructions and the first complete user input.
func DeriveID(format sdktranslator.Format, payload []byte, callerScope string) string {
	if len(payload) == 0 {
		return ""
	}
	var body map[string]any
	if errUnmarshal := json.Unmarshal(payload, &body); errUnmarshal != nil {
		return ""
	}

	root := canonicalRoot{
		Version:     identityVersion,
		Format:      format.String(),
		CallerScope: strings.TrimSpace(callerScope),
	}
	if sourceFormatEqual(format, sdktranslator.FormatGemini) || sourceFormatEqual(format, sdktranslator.FormatAntigravity) {
		reqBody := body
		if req, ok := body["request"].(map[string]any); ok {
			reqBody = req
		}
		root.Resource = stringField(reqBody, "cachedContent", "cached_content")
	}

	switch {
	case sourceFormatEqual(format, sdktranslator.FormatGemini), sourceFormatEqual(format, sdktranslator.FormatAntigravity):
		root.Instructions, root.User = geminiRoot(body)
	case sourceFormatEqual(format, sdktranslator.FormatInteractions):
		root.Instructions, root.User = interactionsRoot(body)
	case sourceFormatEqual(format, sdktranslator.FormatOpenAIResponse), sourceFormatEqual(format, sdktranslator.FormatCodex):
		root.Instructions, root.User = responsesRoot(body)
	case sourceFormatEqual(format, sdktranslator.FormatClaude):
		root.Instructions, root.User = messagesRoot(body, true)
	default:
		root.Instructions, root.User = messagesRoot(body, false)
	}
	if len(root.User) == 0 {
		return ""
	}
	return hashRoot(root)
}

func messagesRoot(body map[string]any, includeTopLevelSystem bool) ([]string, []canonicalPart) {
	instructions := make([]string, 0)
	if includeTopLevelSystem {
		if system, ok := body["system"]; ok {
			instructions = appendInstruction(instructions, system)
		}
	}
	messages, _ := body["messages"].([]any)
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		role := normalizedString(message["role"])
		switch role {
		case "system", "developer":
			instructions = appendInstruction(instructions, message["content"])
		case "user":
			parts := canonicalParts(message["content"])
			if len(parts) > 0 {
				return instructions, parts
			}
		}
	}
	return instructions, nil
}

func responsesRoot(body map[string]any) ([]string, []canonicalPart) {
	instructions := make([]string, 0)
	if value, ok := body["instructions"]; ok {
		instructions = appendInstruction(instructions, value)
	}
	input, ok := body["input"]
	if !ok {
		return instructions, nil
	}
	if inputString, okString := input.(string); okString {
		return instructions, canonicalParts(inputString)
	}
	items, _ := input.([]any)
	for _, rawItem := range items {
		item, okItem := rawItem.(map[string]any)
		if !okItem {
			continue
		}
		role := normalizedString(item["role"])
		switch role {
		case "system", "developer":
			instructions = appendInstruction(instructions, item["content"])
		case "user":
			parts := canonicalParts(item["content"])
			if len(parts) > 0 {
				return instructions, parts
			}
		}
	}
	return instructions, nil
}

func geminiRoot(body map[string]any) ([]string, []canonicalPart) {
	if req, ok := body["request"].(map[string]any); ok {
		body = req
	}
	instructions := make([]string, 0)
	if value, ok := firstField(body, "systemInstruction", "system_instruction"); ok {
		instructions = appendInstruction(instructions, contentValue(value))
	}
	contents, _ := body["contents"].([]any)
	for _, rawContent := range contents {
		content, okContent := rawContent.(map[string]any)
		if !okContent || normalizedString(content["role"]) != "user" {
			continue
		}
		parts := canonicalParts(contentValue(content))
		if len(parts) > 0 {
			return instructions, parts
		}
	}
	return instructions, nil
}

func interactionsRoot(body map[string]any) ([]string, []canonicalPart) {
	instructions := make([]string, 0)
	if value, ok := firstField(body, "system_instruction", "systemInstruction"); ok {
		instructions = appendInstruction(instructions, contentValue(value))
	}
	input, ok := body["input"]
	if !ok {
		return instructions, nil
	}
	if inputString, okString := input.(string); okString {
		return instructions, canonicalParts(inputString)
	}
	for _, entry := range flattenInteractionEntries(input) {
		if text, okString := entry.(string); okString {
			return instructions, canonicalParts(text)
		}
		step, okStep := entry.(map[string]any)
		if !okStep {
			continue
		}
		role := normalizedString(step["role"])
		stepType := normalizedString(step["type"])
		if role == "system" || role == "developer" || stepType == "system_instruction" || stepType == "developer_instruction" {
			instructions = appendInstruction(instructions, contentValue(step))
			continue
		}
		if role == "user" || stepType == "user_input" || ((stepType == "message" || stepType == "") && role == "") {
			return instructions, canonicalParts(contentValue(step))
		}
	}
	return instructions, nil
}

func flattenInteractionEntries(value any) []any {
	entries := make([]any, 0)
	var appendValue func(any, string)
	appendValue = func(current any, inheritedRole string) {
		switch typed := current.(type) {
		case []any:
			for _, child := range typed {
				appendValue(child, inheritedRole)
			}
		case map[string]any:
			role := normalizedString(typed["role"])
			if role == "" {
				role = inheritedRole
			}
			if steps, ok := typed["steps"].([]any); ok {
				for _, child := range steps {
					appendValue(child, role)
				}
				return
			}
			if role != "" && normalizedString(typed["role"]) == "" {
				cloned := make(map[string]any, len(typed)+1)
				for key, child := range typed {
					cloned[key] = child
				}
				cloned["role"] = role
				typed = cloned
			}
			entries = append(entries, typed)
		default:
			entries = append(entries, typed)
		}
	}
	appendValue(value, "")
	return entries
}

func appendInstruction(instructions []string, value any) []string {
	parts := canonicalParts(value)
	var builder strings.Builder
	for _, part := range parts {
		if part.Kind != "text" || part.Value == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(part.Value)
	}
	if builder.Len() == 0 {
		return instructions
	}
	return append(instructions, truncateRunes(builder.String(), instructionRuneLimit))
}

func canonicalParts(value any) []canonicalPart {
	parts := make([]canonicalPart, 0)
	appendCanonicalParts(&parts, value)
	return parts
}

func appendCanonicalParts(parts *[]canonicalPart, value any) {
	switch typed := value.(type) {
	case nil:
		return
	case string:
		if typed != "" {
			*parts = append(*parts, canonicalPart{Kind: "text", Value: typed})
		}
	case []any:
		for _, child := range typed {
			appendCanonicalParts(parts, child)
		}
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			appendCanonicalParts(parts, text)
			return
		}
		if nested, ok := typed["content"]; ok {
			appendCanonicalParts(parts, nested)
			return
		}
		if nested, ok := typed["parts"]; ok {
			appendCanonicalParts(parts, nested)
			return
		}
		if imageURL, ok := typed["image_url"]; ok {
			appendMediaPart(parts, "image", imageURL, "")
			return
		}
		if inlineData, ok := firstField(typed, "inlineData", "inline_data"); ok {
			appendMediaPart(parts, "inline_data", inlineData, "")
			return
		}
		if fileData, ok := firstField(typed, "fileData", "file_data"); ok {
			appendMediaPart(parts, "file", fileData, "")
			return
		}
		if source, ok := typed["source"]; ok {
			appendMediaPart(parts, normalizedString(typed["type"]), source, normalizedString(typed["media_type"]))
			return
		}
		normalized := normalizeJSONValue(typed)
		encoded, errMarshal := json.Marshal(normalized)
		if errMarshal == nil && len(encoded) > 0 {
			*parts = append(*parts, canonicalPart{Kind: "json", Value: string(encoded)})
		}
	default:
		encoded, errMarshal := json.Marshal(typed)
		if errMarshal == nil && len(encoded) > 0 {
			*parts = append(*parts, canonicalPart{Kind: "json", Value: string(encoded)})
		}
	}
}

func appendMediaPart(parts *[]canonicalPart, kind string, value any, fallbackMIME string) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "media"
	}
	switch typed := value.(type) {
	case string:
		if typed != "" {
			*parts = append(*parts, canonicalPart{Kind: kind, MIME: fallbackMIME, Value: typed})
		}
	case map[string]any:
		mime := stringField(typed, "mimeType", "mime_type", "media_type")
		if mime == "" {
			mime = fallbackMIME
		}
		mediaValue := stringField(typed, "url", "uri", "fileUri", "file_uri", "data")
		if mediaValue != "" {
			*parts = append(*parts, canonicalPart{Kind: kind, MIME: mime, Value: mediaValue})
		}
	default:
		appendCanonicalParts(parts, typed)
	}
}

func contentValue(value any) any {
	object, ok := value.(map[string]any)
	if !ok {
		return value
	}
	if content, exists := object["content"]; exists {
		return content
	}
	if parts, exists := object["parts"]; exists {
		return parts
	}
	if text, exists := object["text"]; exists {
		return text
	}
	return object
}

func normalizeJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, child := range typed {
			if strings.EqualFold(strings.TrimSpace(key), "cache_control") {
				continue
			}
			normalized[key] = normalizeJSONValue(child)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for index, child := range typed {
			normalized[index] = normalizeJSONValue(child)
		}
		return normalized
	default:
		return value
	}
}

func hashRoot(root canonicalRoot) string {
	encoded, errMarshal := json.Marshal(root)
	if errMarshal != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return identityPrefix + hex.EncodeToString(sum[:])
}

func metadataWithValue(metadata map[string]any, key string, value any) map[string]any {
	cloned := make(map[string]any, len(metadata)+1)
	for existingKey, existingValue := range metadata {
		cloned[existingKey] = existingValue
	}
	cloned[key] = value
	return cloned
}

func metadataWithoutKey(metadata map[string]any, key string) map[string]any {
	if metadata == nil {
		return nil
	}
	if _, exists := metadata[key]; !exists {
		return metadata
	}
	cloned := make(map[string]any, len(metadata)-1)
	for existingKey, existingValue := range metadata {
		if existingKey != key {
			cloned[existingKey] = existingValue
		}
	}
	return cloned
}

func firstNormalizedMetadataID(key string, metadataSets ...map[string]any) string {
	for _, metadata := range metadataSets {
		if metadata == nil {
			continue
		}
		raw, ok := metadata[key].(string)
		if !ok {
			continue
		}
		if normalized := NormalizeExplicitID(raw); normalized != "" {
			return normalized
		}
	}
	return ""
}

func firstMetadataString(key string, metadataSets ...map[string]any) string {
	for _, metadata := range metadataSets {
		if value := metadataString(metadata, key); value != "" {
			return value
		}
	}
	return ""
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	if text, okText := value.(string); okText {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func firstField(object map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			return value, true
		}
	}
	return nil, false
}

func stringField(object map[string]any, keys ...string) string {
	value, ok := firstField(object, keys...)
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func normalizedString(value any) string {
	text, _ := value.(string)
	return strings.ToLower(strings.TrimSpace(text))
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func sourceFormatEqual(left, right sdktranslator.Format) bool {
	return strings.EqualFold(strings.TrimSpace(left.String()), strings.TrimSpace(right.String()))
}
