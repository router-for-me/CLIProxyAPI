package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (e *AntigravityExecutor) buildRequest(ctx context.Context, auth *cliproxyauth.Auth, token, modelName string, payload []byte, stream bool, alt, baseURL string, derivedSessionIDs ...string) (*http.Request, error) {
	if token == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}

	base := strings.TrimSuffix(baseURL, "/")
	if base == "" {
		base = buildBaseURL(auth)
	}
	path := antigravityGeneratePath
	if stream {
		path = antigravityStreamPath
	}
	var requestURL strings.Builder
	requestURL.WriteString(base)
	requestURL.WriteString(path)
	if stream {
		if alt != "" {
			requestURL.WriteString("?$alt=")
			requestURL.WriteString(url.QueryEscape(alt))
		} else {
			requestURL.WriteString("?alt=sse")
		}
	} else if alt != "" {
		requestURL.WriteString("?$alt=")
		requestURL.WriteString(url.QueryEscape(alt))
	}

	projectID, errProject := e.projectIDForRequest(ctx, auth, token)
	if errProject != nil {
		return nil, errProject
	}
	payload = geminiToAntigravity(modelName, payload, projectID, derivedSessionIDs...)

	// Cap maxOutputTokens to model's max_completion_tokens from registry
	if maxOut := gjson.GetBytes(payload, "request.generationConfig.maxOutputTokens"); maxOut.Exists() && maxOut.Type == gjson.Number {
		if modelInfo := registry.LookupModelInfo(modelName, "antigravity"); modelInfo != nil && modelInfo.MaxCompletionTokens > 0 {
			if int(maxOut.Int()) > modelInfo.MaxCompletionTokens {
				payload, _ = sjson.SetBytes(payload, "request.generationConfig.maxOutputTokens", modelInfo.MaxCompletionTokens)
			}
		}
	}

	useAntigravitySchema := strings.Contains(modelName, "claude") || strings.Contains(modelName, "gemini-3-pro") || strings.Contains(modelName, "gemini-3.1-pro")
	var (
		bodyReader io.Reader
		payloadLog []byte
	)
	if antigravityRequestNeedsSchemaSanitization(payload) {
		payloadStr := sanitizeAntigravityRequestSchemas(string(payload), useAntigravitySchema)

		if strings.Contains(modelName, "claude") {
			updated, _ := sjson.SetBytes([]byte(payloadStr), "request.toolConfig.functionCallingConfig.mode", "VALIDATED")
			payloadStr = string(updated)
		} else {
			payloadStr, _ = sjson.Delete(payloadStr, "request.generationConfig.maxOutputTokens")
		}

		payloadStrBytes := applyAntigravityNativeSignatureReplayIfNeeded(modelName, []byte(payloadStr))
		bodyReader = bytes.NewReader(payloadStrBytes)
		if e.cfg != nil && e.cfg.RequestLog {
			payloadLog = append([]byte(nil), payloadStrBytes...)
		}
	} else {
		if strings.Contains(modelName, "claude") {
			payload, _ = sjson.SetBytes(payload, "request.toolConfig.functionCallingConfig.mode", "VALIDATED")
		} else {
			payload, _ = sjson.DeleteBytes(payload, "request.generationConfig.maxOutputTokens")
		}

		payload = applyAntigravityNativeSignatureReplayIfNeeded(modelName, payload)
		bodyReader = bytes.NewReader(payload)
		if e.cfg != nil && e.cfg.RequestLog {
			payloadLog = append([]byte(nil), payload...)
		}
	}

	// if useAntigravitySchema {
	// 	systemInstructionPartsResult := gjson.Get(payloadStr, "request.systemInstruction.parts")
	// 	payloadStr, _ = sjson.SetBytes([]byte(payloadStr), "request.systemInstruction.role", "user")
	// 	payloadStr, _ = sjson.SetBytes([]byte(payloadStr), "request.systemInstruction.parts.0.text", systemInstruction)
	// 	payloadStr, _ = sjson.SetBytes([]byte(payloadStr), "request.systemInstruction.parts.1.text", fmt.Sprintf("Please ignore following [ignore]%s[/ignore]", systemInstruction))

	// 	if systemInstructionPartsResult.Exists() && systemInstructionPartsResult.IsArray() {
	// 		for _, partResult := range systemInstructionPartsResult.Array() {
	// 			payloadStr, _ = sjson.SetRawBytes([]byte(payloadStr), "request.systemInstruction.parts.-1", []byte(partResult.Raw))
	// 		}
	// 	}
	// }

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bodyReader)
	if errReq != nil {
		return nil, errReq
	}
	httpReq.Close = true
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
	if host := resolveHost(base); host != "" {
		httpReq.Host = host
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       requestURL.String(),
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      payloadLog,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	return httpReq, nil
}

// sanitizeAntigravityRequestSchemas cleans the JSON schemas carried by an Antigravity request.
//
// Cleaning is applied only to the payload locations that actually hold a JSON schema. The schema
// cleaner rewrites keys such as "title", "format", "default" and "const", which are also ordinary
// data keys inside functionCall arguments replayed from conversation history. Running it over the
// whole document silently mutated that history, so tools lost required argument fields and the
// model imitated the corrupted examples on later turns.
func sanitizeAntigravityRequestSchemas(payloadStr string, useAntigravitySchema bool) string {
	for _, base := range antigravityFunctionDeclarationPaths(payloadStr) {
		oldPath := base + ".parametersJsonSchema"
		if !gjson.Get(payloadStr, oldPath).Exists() {
			continue
		}
		renamed, errRename := util.RenameKey(payloadStr, oldPath, base+".parameters")
		if errRename != nil {
			log.Debugf("antigravity: failed to rename %s: %v", oldPath, errRename)
			continue
		}
		payloadStr = renamed
	}

	toolSchemaCleaner := func(schema string) string {
		return util.CleanJSONSchemaForAntigravityTool(schema, useAntigravitySchema)
	}

	sanitizeThenClean := func(schemaRaw string, clean func(string) string) string {
		sanitized, errSanitize := sanitizeAntigravityBooleanSubschemas([]byte(schemaRaw))
		if errSanitize != nil {
			log.Debugf("antigravity: failed to sanitize boolean subschemas: %v", errSanitize)
			return clean(schemaRaw)
		}
		return clean(string(sanitized))
	}
	cleanNestedToolSchema := func(schemaRaw string) string {
		return sanitizeThenClean(schemaRaw, func(sanitized string) string {
			return cleanNestedSchema(toolSchemaCleaner, sanitized)
		})
	}
	cleanResponseSchema := func(schemaRaw string) string {
		return sanitizeThenClean(schemaRaw, util.CleanJSONSchemaForAntigravityResponse)
	}
	payloadStr = cleanAntigravitySchemasAtPaths(payloadStr, antigravityDeclarationSchemaPaths(payloadStr), cleanNestedToolSchema)
	payloadStr = cleanAntigravitySchemasAtPaths(payloadStr, antigravityGenerationSchemaPaths(payloadStr), cleanResponseSchema)
	return payloadStr
}

// sanitizeAntigravityBooleanSubschemas replaces true schemas with their equivalent empty object.
// False schemas and booleans outside schema-bearing keywords are preserved.
func sanitizeAntigravityBooleanSubschemas(raw []byte) ([]byte, error) {
	return sanitizeAntigravitySchemaNode(json.RawMessage(raw))
}

func sanitizeAntigravitySchemaNode(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("true")) {
		return json.RawMessage(`{}`), nil
	}
	if bytes.Equal(trimmed, []byte("false")) {
		return append(json.RawMessage(nil), raw...), nil
	}
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty schema")
	}

	switch trimmed[0] {
	case '{':
		var schema map[string]json.RawMessage
		if errUnmarshal := json.Unmarshal(trimmed, &schema); errUnmarshal != nil {
			return nil, fmt.Errorf("decode schema object: %w", errUnmarshal)
		}

		changed := false
		for _, keyword := range []string{"anyOf", "oneOf"} {
			child, ok := schema[keyword]
			if !ok {
				continue
			}
			normalized, complement, remove, unsatisfiable, errNormalize := normalizeAntigravityBooleanUnion(keyword, child)
			if errNormalize != nil {
				return nil, fmt.Errorf("normalize %s: %w", keyword, errNormalize)
			}
			if unsatisfiable {
				return json.RawMessage("false"), nil
			}
			if complement != nil {
				if _, hasExistingNot := schema["not"]; hasExistingNot {
					// Combining complements requires a union that the downstream cleaner flattens
					// lossily, so retain the existing compatibility behavior for this rare shape.
					normalized = child
				} else {
					delete(schema, keyword)
					schema["not"] = complement
					changed = true
					continue
				}
			}
			if remove {
				delete(schema, keyword)
				changed = true
				continue
			}
			if !bytes.Equal(normalized, child) {
				schema[keyword] = normalized
				changed = true
			}
		}

		for _, keyword := range antigravitySingleSchemaKeywords {
			child, ok := schema[keyword]
			if !ok {
				continue
			}
			sanitized, errSanitize := sanitizeAntigravitySchemaNode(child)
			if errSanitize != nil {
				return nil, fmt.Errorf("sanitize %s: %w", keyword, errSanitize)
			}
			if !bytes.Equal(sanitized, child) {
				schema[keyword] = sanitized
				changed = true
			}
		}

		for _, keyword := range antigravitySchemaMapKeywords {
			rawChildren, ok := schema[keyword]
			if !ok {
				continue
			}
			var children map[string]json.RawMessage
			if errUnmarshal := json.Unmarshal(rawChildren, &children); errUnmarshal != nil {
				continue
			}
			childrenChanged := false
			for name, child := range children {
				if keyword == "dependencies" && firstJSONToken(child) == '[' {
					continue
				}
				sanitized, errSanitize := sanitizeAntigravitySchemaNode(child)
				if errSanitize != nil {
					return nil, fmt.Errorf("sanitize %s.%s: %w", keyword, name, errSanitize)
				}
				if !bytes.Equal(sanitized, child) {
					children[name] = sanitized
					childrenChanged = true
				}
			}
			if childrenChanged {
				updated, errMarshal := json.Marshal(children)
				if errMarshal != nil {
					return nil, fmt.Errorf("encode %s: %w", keyword, errMarshal)
				}
				schema[keyword] = updated
				changed = true
			}
		}

		if !changed {
			return append(json.RawMessage(nil), raw...), nil
		}
		updated, errMarshal := json.Marshal(schema)
		if errMarshal != nil {
			return nil, fmt.Errorf("encode schema object: %w", errMarshal)
		}
		return updated, nil

	case '[':
		var schemas []json.RawMessage
		if errUnmarshal := json.Unmarshal(trimmed, &schemas); errUnmarshal != nil {
			return nil, fmt.Errorf("decode schema array: %w", errUnmarshal)
		}
		changed := false
		for i, child := range schemas {
			sanitized, errSanitize := sanitizeAntigravitySchemaNode(child)
			if errSanitize != nil {
				return nil, fmt.Errorf("sanitize schema array item %d: %w", i, errSanitize)
			}
			if !bytes.Equal(sanitized, child) {
				schemas[i] = sanitized
				changed = true
			}
		}
		if !changed {
			return append(json.RawMessage(nil), raw...), nil
		}
		updated, errMarshal := json.Marshal(schemas)
		if errMarshal != nil {
			return nil, fmt.Errorf("encode schema array: %w", errMarshal)
		}
		return updated, nil

	default:
		if !json.Valid(trimmed) {
			return nil, fmt.Errorf("invalid schema JSON")
		}
		return append(json.RawMessage(nil), raw...), nil
	}
}

// normalizeAntigravityBooleanUnion simplifies boolean members before true schemas become empty
// objects. Removing a tautological union preserves constraints carried by its parent schema.
func normalizeAntigravityBooleanUnion(keyword string, raw json.RawMessage) (normalized, complement json.RawMessage, remove, unsatisfiable bool, err error) {
	var branches []json.RawMessage
	if errUnmarshal := json.Unmarshal(raw, &branches); errUnmarshal != nil {
		return nil, nil, false, false, fmt.Errorf("decode boolean union: %w", errUnmarshal)
	}

	trueCount := 0
	falseCount := 0
	nonBoolean := make([]json.RawMessage, 0, len(branches))
	for _, branch := range branches {
		trimmed := bytes.TrimSpace(branch)
		switch {
		case bytes.Equal(trimmed, []byte("true")):
			trueCount++
		case bytes.Equal(trimmed, []byte("false")):
			falseCount++
		default:
			nonBoolean = append(nonBoolean, branch)
		}
	}
	if trueCount == 0 && falseCount == 0 {
		return append(json.RawMessage(nil), raw...), nil, false, false, nil
	}

	switch keyword {
	case "anyOf":
		if trueCount > 0 {
			return nil, nil, true, false, nil
		}
		if len(nonBoolean) == 0 {
			return nil, nil, false, true, nil
		}
	case "oneOf":
		if trueCount > 1 || trueCount == 0 && len(nonBoolean) == 0 {
			return nil, nil, false, true, nil
		}
		if trueCount == 1 {
			if len(nonBoolean) == 0 {
				return nil, nil, true, false, nil
			}
			if len(nonBoolean) == 1 {
				return nil, append(json.RawMessage(nil), nonBoolean[0]...), false, false, nil
			}
			// A multi-branch complement needs a nested union that the downstream cleaner flattens
			// lossily, so retain the existing compatibility behavior for that shape.
			return append(json.RawMessage(nil), raw...), nil, false, false, nil
		}
	}

	updated, errMarshal := json.Marshal(nonBoolean)
	if errMarshal != nil {
		return nil, nil, false, false, fmt.Errorf("encode boolean union: %w", errMarshal)
	}
	return updated, nil, false, false, nil
}

func firstJSONToken(raw json.RawMessage) byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0
	}
	return trimmed[0]
}

var (
	antigravitySingleSchemaKeywords = []string{
		"additionalItems", "additionalProperties", "contains", "contentSchema",
		"if", "then", "else", "not", "propertyNames", "items",
		"unevaluatedItems", "unevaluatedProperties",
		"prefixItems", "allOf", "anyOf", "oneOf",
	}
	antigravitySchemaMapKeywords = []string{
		"properties", "patternProperties", "dependentSchemas", "dependencies",
		"$defs", "definitions",
	}
)

func cleanAntigravitySchemasAtPaths(payloadStr string, schemaPaths []string, clean func(string) string) string {
	for _, schemaPath := range schemaPaths {
		schema := gjson.Get(payloadStr, schemaPath)
		if !schema.Exists() {
			continue
		}
		updated, errSet := sjson.SetRawBytes([]byte(payloadStr), schemaPath, []byte(clean(schema.Raw)))
		if errSet != nil {
			log.Debugf("antigravity: failed to write cleaned schema at %s: %v", schemaPath, errSet)
			continue
		}
		payloadStr = string(updated)
	}
	return payloadStr
}

// antigravitySchemaWrapperKey nests a schema during cleaning. It is never sent upstream.
const antigravitySchemaWrapperKey = "schema"

// cleanNestedSchema cleans a schema with it nested one level down, then unwraps it.
//
// The cleaner deliberately skips placeholder insertion for a top-level schema, but Claude's
// VALIDATED mode needs every tool schema to declare at least one required property. Whole-payload
// cleaning always saw tool schemas nested inside the request, so nesting is reproduced here to keep
// the emitted schema byte-identical to the previous behaviour.
func cleanNestedSchema(clean func(string) string, schemaRaw string) string {
	wrapped, errWrap := sjson.SetRaw("{}", antigravitySchemaWrapperKey, schemaRaw)
	if errWrap != nil {
		return clean(schemaRaw)
	}
	if unwrapped := gjson.Get(clean(wrapped), antigravitySchemaWrapperKey); unwrapped.Exists() {
		return unwrapped.Raw
	}
	return clean(schemaRaw)
}

// antigravityFunctionDeclarationPaths returns the path of every function declaration in the request.
// Both the camelCase and snake_case spellings are accepted because callers reach this executor
// through different translators.
func antigravityFunctionDeclarationPaths(payloadStr string) []string {
	tools := gjson.Get(payloadStr, "request.tools")
	if !tools.IsArray() {
		return nil
	}
	paths := make([]string, 0, len(tools.Array()))
	for i, tool := range tools.Array() {
		for _, declKey := range []string{"functionDeclarations", "function_declarations"} {
			decls := tool.Get(declKey)
			if !decls.IsArray() {
				continue
			}
			for j := range decls.Array() {
				paths = append(paths, fmt.Sprintf("request.tools.%d.%s.%d", i, declKey, j))
			}
		}
	}
	return paths
}

// antigravitySchemaPaths returns every payload path that holds a JSON schema document.
// A function declaration may carry a schema for its parameters and for its result, so all of
// them must be cleaned; anything omitted here reaches the upstream API uncleaned.
func antigravitySchemaPaths(payloadStr string) []string {
	paths := antigravityDeclarationSchemaPaths(payloadStr)
	return append(paths, antigravityGenerationSchemaPaths(payloadStr)...)
}

func antigravityDeclarationSchemaPaths(payloadStr string) []string {
	paths := make([]string, 0, 8)
	for _, base := range antigravityFunctionDeclarationPaths(payloadStr) {
		for _, key := range antigravityDeclarationSchemaKeys {
			schema := gjson.Get(payloadStr, base+"."+key)
			if schema.IsObject() || schema.Type == gjson.True || schema.Type == gjson.False {
				paths = append(paths, base+"."+key)
			}
		}
	}
	return paths
}

func antigravityGenerationSchemaPaths(payloadStr string) []string {
	paths := make([]string, 0, len(antigravityGenerationConfigContainers)*len(antigravityGenerationSchemaKeys))
	for _, container := range antigravityGenerationConfigContainers {
		for _, key := range antigravityGenerationSchemaKeys {
			path := container + "." + key
			schema := gjson.Get(payloadStr, path)
			if schema.IsObject() || schema.Type == gjson.True || schema.Type == gjson.False {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

// The upstream API is proto-JSON and accepts either spelling, and the Gemini translator forwards
// whichever one the client sent. Both are therefore cleaned where they sit rather than renamed:
// renaming would alter the body the client asked for, and only the unsupported keywords inside a
// schema cause upstream errors. The one exception is parametersJsonSchema, renamed onto parameters
// above because whole-payload cleaning did the same.
var (
	antigravityDeclarationSchemaKeys = []string{
		"parameters", "parametersJsonSchema", "parameters_json_schema",
		"response", "responseJsonSchema", "response_json_schema",
	}
	antigravityGenerationConfigContainers = []string{
		"request.generationConfig", "request.generation_config",
	}
	antigravityGenerationSchemaKeys = []string{
		"responseSchema", "responseJsonSchema", "response_schema", "response_json_schema",
	}
)

func antigravityRequestNeedsSchemaSanitization(payload []byte) bool {
	if gjson.GetBytes(payload, "request.tools.0").Exists() {
		return true
	}
	for _, container := range antigravityGenerationConfigContainers {
		for _, key := range antigravityGenerationSchemaKeys {
			if gjson.GetBytes(payload, container+"."+key).Exists() {
				return true
			}
		}
	}
	return false
}
func buildBaseURL(auth *cliproxyauth.Auth) string {
	if baseURLs := antigravityBaseURLFallbackOrder(auth); len(baseURLs) > 0 {
		return baseURLs[0]
	}
	return antigravityBaseURLDaily
}

func antigravityLoadCodeAssistBaseURL(auth *cliproxyauth.Auth) string {
	if base := resolveCustomAntigravityBaseURL(auth); base != "" {
		return base
	}
	return antigravityBaseURLProd
}

func resolveHost(base string) string {
	parsed, errParse := url.Parse(base)
	if errParse != nil {
		return ""
	}
	if parsed.Host != "" {
		return parsed.Host
	}
	return strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
}

func resolveUserAgent(auth *cliproxyauth.Auth) string {
	return misc.AntigravityRequestUserAgent(antigravityConfiguredUserAgent(auth))
}

func resolveLoadCodeAssistUserAgent(auth *cliproxyauth.Auth) string {
	return misc.AntigravityLoadCodeAssistUserAgent(antigravityConfiguredUserAgent(auth))
}

func antigravityConfiguredUserAgent(auth *cliproxyauth.Auth) string {
	raw := ""
	if auth != nil {
		if auth.Attributes != nil {
			if ua := strings.TrimSpace(auth.Attributes["user_agent"]); ua != "" {
				raw = ua
			}
		}
		if raw == "" && auth.Metadata != nil {
			if ua, ok := auth.Metadata["user_agent"].(string); ok && strings.TrimSpace(ua) != "" {
				raw = strings.TrimSpace(ua)
			}
		}
	}
	return raw
}

var antigravityBaseURLFallbackOrder = func(auth *cliproxyauth.Auth) []string {
	if base := resolveCustomAntigravityBaseURL(auth); base != "" {
		return []string{base}
	}
	return []string{
		antigravityBaseURLDaily,
		antigravityBaseURLProd,
		// antigravitySandboxBaseURLDaily,
	}
}

func resolveCustomAntigravityBaseURL(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["base_url"]); v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["base_url"].(string); ok {
			v = strings.TrimSpace(v)
			if v != "" {
				return strings.TrimSuffix(v, "/")
			}
		}
	}
	return ""
}

func geminiToAntigravity(modelName string, payload []byte, projectID string, derivedSessionIDs ...string) []byte {
	template := payload
	template = helps.SetStringIfDifferent(template, "model", modelName)
	template = helps.SetStringIfDifferent(template, "userAgent", "antigravity")

	isImageModel := strings.Contains(modelName, "image")
	reqType := strings.TrimSpace(gjson.GetBytes(template, "requestType").String())
	if reqType == "" {
		if isImageModel {
			reqType = "image_gen"
		} else {
			reqType = "agent"
		}
		template, _ = sjson.SetBytes(template, "requestType", reqType)
	}

	if projectID != "" {
		template = helps.SetStringIfDifferent(template, "project", projectID)
	} else {
		template, _ = sjson.DeleteBytes(template, "project")
	}

	if isImageModel {
		template, _ = sjson.SetBytes(template, "requestId", generateImageGenRequestID())
	} else if reqType != "web_search" {
		template, _ = sjson.SetBytes(template, "requestId", generateRequestID())
		sessionID := strings.TrimSpace(gjson.GetBytes(template, "request.sessionId").String())
		if sessionID == "" && len(derivedSessionIDs) > 0 {
			sessionID = strings.TrimSpace(derivedSessionIDs[0])
		}
		if sessionID == "" {
			sessionID = generateStableSessionID(payload)
		}
		template, _ = sjson.SetBytes(template, "request.sessionId", sessionID)
	}

	template, _ = sjson.DeleteBytes(template, "request.safetySettings")
	if toolConfig := gjson.GetBytes(template, "toolConfig"); toolConfig.Exists() && !gjson.GetBytes(template, "request.toolConfig").Exists() {
		template, _ = sjson.SetRawBytes(template, "request.toolConfig", []byte(toolConfig.Raw))
		template, _ = sjson.DeleteBytes(template, "toolConfig")
	}
	return template
}

func generateRequestID() string {
	return "agent-" + uuid.NewString()
}

func generateImageGenRequestID() string {
	return fmt.Sprintf("image_gen/%d/%s/12", time.Now().UnixMilli(), uuid.NewString())
}

func generateSessionID() string {
	randSourceMutex.Lock()
	n := randSource.Int63n(9_000_000_000_000_000_000)
	randSourceMutex.Unlock()
	return "-" + strconv.FormatInt(n, 10)
}

func generateStableSessionID(payload []byte) string {
	contents := gjson.GetBytes(payload, "request.contents")
	if contents.IsArray() {
		for _, content := range contents.Array() {
			if content.Get("role").String() == "user" {
				text := content.Get("parts.0.text").String()
				if text != "" {
					h := sha256.Sum256([]byte(text))
					n := int64(binary.BigEndian.Uint64(h[:8])) & 0x7FFFFFFFFFFFFFFF
					return "-" + strconv.FormatInt(n, 10)
				}
			}
		}
	}
	return generateSessionID()
}
