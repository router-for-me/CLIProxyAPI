package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
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
		base = resolveAntigravityRequestBaseURL(auth)
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

	isClaudeModel := strings.Contains(modelName, "claude")
	useAntigravitySchema := isClaudeModel || strings.Contains(modelName, "gemini-3-pro") || strings.Contains(modelName, "gemini-3.1-pro")
	var (
		bodyReader io.Reader
		payloadLog []byte
	)
	if antigravityRequestNeedsSchemaSanitization(payload) {
		payloadStr := sanitizeAntigravityRequestSchemasForModel(string(payload), useAntigravitySchema, isClaudeModel)

		if strings.Contains(modelName, "claude") {
			updated, _ := sjson.SetBytes([]byte(payloadStr), "request.toolConfig.functionCallingConfig.mode", "VALIDATED")
			payloadStr = string(updated)
		} else {
			payloadStr, _ = sjson.Delete(payloadStr, "request.generationConfig.maxOutputTokens")
		}

		payloadStrBytes := applyAntigravityNativeSignatureReplayIfNeeded(modelName, []byte(payloadStr))
		payloadStrBytes = helps.FinalizePayload(ctx, payloadStrBytes)
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
		payload = helps.FinalizePayload(ctx, payload)
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
	// Deliberately no httpReq.Close: the native Antigravity client omits the
	// Connection header and keeps its HTTP/1.1 connections alive, so forcing
	// "Connection: close" would both deviate from that fingerprint and defeat the
	// shared connection pool by discarding every established TCP + TLS session.
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
// sanitizeAntigravityRequestSchemas cleans with union-preserving (Gemini) tool semantics.
// Claude tool unions are flattened only when the caller supplies the model flag via
// sanitizeAntigravityRequestSchemasForModel; the placeholder group alone must not decide it,
// because gemini-3-pro models share that group and accept preserved unions.
func sanitizeAntigravityRequestSchemas(payloadStr string, useAntigravitySchema bool) string {
	return sanitizeAntigravityRequestSchemasForModel(payloadStr, useAntigravitySchema, false)
}

// sanitizeAntigravityRequestSchemasForModel applies sanitizeAntigravityRequestSchemas with an
// explicit Antigravity Claude flag. Antigravity Claude tool schemas additionally flatten
// multi-branch anyOf/oneOf unions, which the Claude-side upstream rejects (Issue 3662).
func sanitizeAntigravityRequestSchemasForModel(payloadStr string, useAntigravitySchema, isClaudeModel bool) string {
	payloadStr = sanitizeAntigravityToolSchemasForModel(payloadStr, useAntigravitySchema, isClaudeModel)
	return sanitizeAntigravityGenerationSchemas(payloadStr)
}

// sanitizeAntigravityToolSchemas applies the existing declaration rewrites to
// a small document containing only request.tools, then replaces that subtree
// once. This preserves rewrite order and bytes without copying the full request
// for every declaration schema.
func sanitizeAntigravityToolSchemas(payloadStr string, useAntigravitySchema bool) string {
	return sanitizeAntigravityToolSchemasForModel(payloadStr, useAntigravitySchema, false)
}

func sanitizeAntigravityToolSchemasForModel(payloadStr string, useAntigravitySchema, isClaudeModel bool) string {
	tools := gjson.Get(payloadStr, "request.tools")
	if !tools.IsArray() {
		return payloadStr
	}

	toolDocument := `{"request":{"tools":` + tools.Raw + `}}`
	toolDocument = sanitizeAntigravityToolSchemaDocumentForModel(toolDocument, useAntigravitySchema, isClaudeModel)
	cleanedTools := gjson.Get(toolDocument, "request.tools")
	if !cleanedTools.IsArray() || cleanedTools.Raw == tools.Raw {
		return payloadStr
	}
	updated, errSet := sjson.SetRawBytes([]byte(payloadStr), "request.tools", []byte(cleanedTools.Raw))
	if errSet != nil {
		log.Debugf("antigravity: failed to write cleaned request.tools: %v", errSet)
		return payloadStr
	}
	return string(updated)
}

func sanitizeAntigravityToolSchemaDocument(payloadStr string, useAntigravitySchema bool) string {
	return sanitizeAntigravityToolSchemaDocumentForModel(payloadStr, useAntigravitySchema, false)
}

// antigravityDeclarationContainers lists both spellings of the declarations
// array a tool may carry.
var antigravityDeclarationContainers = []string{"functionDeclarations", "function_declarations"}

// sanitizeAntigravityToolSchemaDocumentForModel renames and cleans every
// declaration schema in the tools-only document. Each declaration is edited
// on its own raw bytes and the results are spliced back into the tools array
// in a single pass, so the cost stays linear in the document size instead of
// scaling with declarations x document size (Issue 6714). The emitted bytes
// match the previous whole-document edits: changed elements are spliced at
// their original offsets, so untouched bytes (including whitespace around
// array elements) survive verbatim, and changed nodes go through the same
// sjson edits, scoped to one declaration.
func sanitizeAntigravityToolSchemaDocumentForModel(payloadStr string, useAntigravitySchema, isClaudeModel bool) string {
	toolSchemaCleaner := func(schema string, isParameterSchema bool) string {
		// Only declaration parameter schemas become Claude custom.input_schema upstream,
		// so only they need Claude union flattening. Function return schemas keep the
		// union-preserving cleaning like every other response-side schema.
		if isClaudeModel && isParameterSchema {
			return util.CleanJSONSchemaForAntigravityClaudeTool(schema, useAntigravitySchema)
		}
		return util.CleanJSONSchemaForAntigravityTool(schema, useAntigravitySchema)
	}
	cleanNestedToolSchema := func(schemaRaw string, isParameterSchema bool) string {
		return cleanNestedSchema(func(schema string) string {
			return toolSchemaCleaner(schema, isParameterSchema)
		}, schemaRaw)
	}

	tools := gjson.Parse(payloadStr).Get("request.tools")
	if !tools.IsArray() {
		return payloadStr
	}
	elements := make([]gjson.Result, 0)
	edits := make([]antigravityElementEdit, 0)
	tools.ForEach(func(_, tool gjson.Result) bool {
		elements = append(elements, tool)
		if !tool.IsObject() {
			return true
		}
		toolRaw := tool.Raw
		for _, declKey := range antigravityDeclarationContainers {
			decls := tool.Get(declKey)
			if !decls.IsArray() {
				continue
			}
			cleanedDecls, declsChanged := sanitizeAntigravityDeclarations(decls, cleanNestedToolSchema)
			if !declsChanged {
				continue
			}
			updated, errSet := sjson.SetRawBytes([]byte(toolRaw), declKey, []byte(cleanedDecls))
			if errSet != nil {
				log.Debugf("antigravity: failed to write cleaned %s: %v", declKey, errSet)
				continue
			}
			toolRaw = string(updated)
		}
		if toolRaw != tool.Raw {
			edits = append(edits, antigravityElementEdit{offset: tool.Index - tools.Index, raw: tool.Raw, replacement: toolRaw})
		}
		return true
	})
	if len(edits) == 0 {
		return payloadStr
	}
	updated, errSet := sjson.SetRawBytes([]byte(payloadStr), "request.tools", []byte(applyAntigravityElementEdits(tools.Raw, tools.Index, elements, edits)))
	if errSet != nil {
		log.Debugf("antigravity: failed to write cleaned request.tools: %v", errSet)
		return payloadStr
	}
	return string(updated)
}

// antigravityElementEdit replaces one array element (raw, at offset within
// the array's raw bytes) with replacement.
type antigravityElementEdit struct {
	offset      int
	raw         string
	replacement string
}

// applyAntigravityElementEdits splices element replacements into an array's
// raw bytes in offset order, keeping every other byte (commas, whitespace,
// unchanged elements) verbatim. Edits whose offsets do not line up with the
// array bytes fall back to joining the elements with commas: still valid
// and semantically identical, just without the original separators. gjson
// reports element offsets into the parsed document, so offsets here are
// relative to the array start.
func applyAntigravityElementEdits(arrayRaw string, arrayIndex int, elements []gjson.Result, edits []antigravityElementEdit) string {
	var rebuilt strings.Builder
	rebuilt.Grow(len(arrayRaw))
	cursor := 0
	for _, edit := range edits {
		if edit.offset < cursor || edit.offset+len(edit.raw) > len(arrayRaw) || arrayRaw[edit.offset:edit.offset+len(edit.raw)] != edit.raw {
			return joinAntigravityElements(arrayIndex, elements, edits)
		}
		rebuilt.WriteString(arrayRaw[cursor:edit.offset])
		rebuilt.WriteString(edit.replacement)
		cursor = edit.offset + len(edit.raw)
	}
	rebuilt.WriteString(arrayRaw[cursor:])
	return rebuilt.String()
}

// joinAntigravityElements rebuilds an array by joining element bytes with
// commas, substituting replacements where recorded. It is the fallback for
// applyAntigravityElementEdits when splice offsets cannot be trusted.
func joinAntigravityElements(arrayIndex int, elements []gjson.Result, edits []antigravityElementEdit) string {
	replacements := make(map[int]string, len(edits))
	for _, edit := range edits {
		replacements[edit.offset] = edit.replacement
	}
	var rebuilt strings.Builder
	rebuilt.WriteByte('[')
	for i, element := range elements {
		if i > 0 {
			rebuilt.WriteByte(',')
		}
		if replacement, ok := replacements[element.Index-arrayIndex]; ok {
			rebuilt.WriteString(replacement)
		} else {
			rebuilt.WriteString(element.Raw)
		}
	}
	rebuilt.WriteByte(']')
	return rebuilt.String()
}

// sanitizeAntigravityDeclarations rebuilds one declarations array, applying
// the rename and schema cleaning to each declaration in isolation. It
// reports whether any declaration actually changed so callers can keep the
// original bytes when the array is already clean.
func sanitizeAntigravityDeclarations(decls gjson.Result, clean func(schemaRaw string, isParameterSchema bool) string) (string, bool) {
	elements := make([]gjson.Result, 0)
	edits := make([]antigravityElementEdit, 0)
	decls.ForEach(func(_, decl gjson.Result) bool {
		elements = append(elements, decl)
		if !decl.IsObject() {
			return true
		}
		cleanedDecl := sanitizeAntigravityDeclaration(decl, clean)
		if cleanedDecl != decl.Raw {
			edits = append(edits, antigravityElementEdit{offset: decl.Index - decls.Index, raw: decl.Raw, replacement: cleanedDecl})
		}
		return true
	})
	if len(edits) == 0 {
		return "", false
	}
	return applyAntigravityElementEdits(decls.Raw, decls.Index, elements, edits), true
}

// sanitizeAntigravityDeclaration applies the declaration-level edits that
// used to run against the whole tools document: parametersJsonSchema is
// renamed onto parameters first, then every object schema is cleaned in
// place. Failed edits keep the declaration as-is and are only logged.
func sanitizeAntigravityDeclaration(decl gjson.Result, clean func(schemaRaw string, isParameterSchema bool) string) string {
	declRaw := decl.Raw
	if decl.Get("parametersJsonSchema").Exists() {
		renamed, errRename := util.RenameKey(declRaw, "parametersJsonSchema", "parameters")
		if errRename != nil {
			log.Debugf("antigravity: failed to rename parametersJsonSchema: %v", errRename)
		} else {
			declRaw = renamed
			decl = gjson.Parse(declRaw)
		}
	}
	for _, key := range antigravityDeclarationSchemaKeys {
		schema := decl.Get(key)
		if !schema.IsObject() {
			continue
		}
		cleanedSchema := clean(schema.Raw, isParameterSchemaPath(key))
		if cleanedSchema == schema.Raw {
			continue
		}
		updated, errSet := sjson.SetRawBytes([]byte(declRaw), key, []byte(cleanedSchema))
		if errSet != nil {
			log.Debugf("antigravity: failed to write cleaned schema at %s: %v", key, errSet)
			continue
		}
		declRaw = string(updated)
		decl = gjson.Parse(declRaw)
	}
	return declRaw
}

// sanitizeAntigravityGenerationSchemas batches every schema edit within one
// generation config before replacing that config in the full request.
func sanitizeAntigravityGenerationSchemas(payloadStr string) string {
	for _, container := range antigravityGenerationConfigContainers {
		generationConfig := gjson.Get(payloadStr, container)
		if !generationConfig.IsObject() {
			continue
		}
		cleanedConfig := generationConfig.Raw
		for _, key := range antigravityGenerationSchemaKeys {
			schema := gjson.Get(cleanedConfig, key)
			if !schema.IsObject() {
				continue
			}
			cleanedSchema := util.CleanJSONSchemaForAntigravityResponse(schema.Raw)
			if cleanedSchema == schema.Raw {
				continue
			}
			updated, errSet := sjson.SetRawBytes([]byte(cleanedConfig), key, []byte(cleanedSchema))
			if errSet != nil {
				log.Debugf("antigravity: failed to write cleaned schema at %s.%s: %v", container, key, errSet)
				continue
			}
			cleanedConfig = string(updated)
		}
		if cleanedConfig == generationConfig.Raw {
			continue
		}
		updated, errSet := sjson.SetRawBytes([]byte(payloadStr), container, []byte(cleanedConfig))
		if errSet != nil {
			log.Debugf("antigravity: failed to write cleaned %s: %v", container, errSet)
			continue
		}
		payloadStr = string(updated)
	}
	return payloadStr
}

// isParameterSchemaPath reports whether a declaration schema path holds tool input
// parameters rather than a function return schema.
func isParameterSchemaPath(schemaPath string) bool {
	key := schemaPath
	if idx := strings.LastIndex(schemaPath, "."); idx >= 0 {
		key = schemaPath[idx+1:]
	}
	return strings.HasPrefix(key, "parameters")
}

func cleanAntigravitySchemasAtPaths(payloadStr string, schemaPaths []string, clean func(string) string) string {
	for _, schemaPath := range schemaPaths {
		schema := gjson.Get(payloadStr, schemaPath)
		if !schema.Exists() {
			continue
		}
		cleanedSchema := clean(schema.Raw)
		if cleanedSchema == schema.Raw {
			continue
		}
		updated, errSet := sjson.SetRawBytes([]byte(payloadStr), schemaPath, []byte(cleanedSchema))
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
			if gjson.Get(payloadStr, base+"."+key).IsObject() {
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
			if gjson.Get(payloadStr, path).IsObject() {
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

// resolveAntigravityRequestBaseURL selects one request endpoint without cross-tier fallback.
// Consumer credentials default to daily; enterprise/GCP credentials can set base_url explicitly.
func resolveAntigravityRequestBaseURL(auth *cliproxyauth.Auth) string {
	if base := resolveCustomAntigravityBaseURL(auth); base != "" {
		return base
	}
	return antigravityBaseURLDaily
}

func antigravityLoadCodeAssistBaseURL(auth *cliproxyauth.Auth) string {
	return resolveAntigravityRequestBaseURL(auth)
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
	contents := util.GetGJSONBytesNoCopy(payload, "request.contents")
	if !contents.IsArray() {
		return generateSessionID()
	}

	stableID := ""
	contents.ForEach(func(_, content gjson.Result) bool {
		if content.Get("role").String() != "user" {
			return true
		}
		text := content.Get("parts.0.text").String()
		if text == "" {
			return true
		}
		hash := sha256.Sum256([]byte(text))
		value := int64(binary.BigEndian.Uint64(hash[:8])) & 0x7FFFFFFFFFFFFFFF
		stableID = "-" + strconv.FormatInt(value, 10)
		return false
	})
	if stableID != "" {
		return stableID
	}
	return generateSessionID()
}
