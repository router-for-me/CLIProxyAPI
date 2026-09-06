package executor

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAICompatFallbackCapsulePrefix = "cpa-compat-compaction-v1."

var openAICompatFallbackSummaryInstruction = `Create a compact continuation summary of the conversation above. Preserve user requirements, decisions, constraints, file paths, identifiers, commands, errors, tool results, and unfinished work. Do not continue the task or add commentary. Return only the summary.`

type openAICompatFallbackCapsule struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Summary  string `json:"summary"`
}

func (e *OpenAICompatExecutor) fallbackCompactionEnabled(auth *cliproxyauth.Auth) bool {
	compat := e.resolveCompatConfig(auth)
	return compat != nil && compat.FallbackCompaction
}

func (e *OpenAICompatExecutor) executeFallbackCompaction(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
	}
	payload, errExpand := e.expandFallbackCompactionCapsules(auth, req.Payload)
	if errExpand != nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	payload = removeResponsesInputType(payload, "compaction_trigger")
	translated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FromString("openai"), req.Model, payload, false, helps.APIKeyModelIsCompat(req))
	translated, _ = sjson.SetBytes(translated, "stream", false)
	translated, _ = sjson.DeleteBytes(translated, "tools")
	translated, _ = sjson.DeleteBytes(translated, "tool_choice")
	translated, _ = sjson.DeleteBytes(translated, "parallel_tool_calls")
	translated, _ = sjson.SetBytes(translated, "messages.-1", map[string]any{"role": "user", "content": openAICompatFallbackSummaryInstruction})

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if errRequest != nil {
		return resp, errRequest
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		return resp, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			helps.LogWithRequestID(ctx).Warnf("openai compat fallback compaction: close response body: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		return resp, errRead
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return resp, statusErr{code: httpResp.StatusCode, msg: string(body)}
	}
	summary := openAICompatFallbackResponseText(body)
	if summary == "" {
		return resp, statusErr{code: http.StatusBadGateway, msg: "fallback compaction upstream returned no summary"}
	}
	capsule, errSeal := e.sealFallbackCompactionCapsule(auth, req.Model, summary)
	if errSeal != nil {
		return resp, errSeal
	}
	result := []byte(`{"id":"","object":"response.compaction","created_at":0,"output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`)
	result, _ = sjson.SetBytes(result, "id", "resp_compact_"+strings.ReplaceAll(uuid.NewString(), "-", ""))
	result, _ = sjson.SetBytes(result, "created_at", time.Now().Unix())
	result, _ = sjson.SetBytes(result, "model", req.Model)
	result, _ = sjson.SetBytes(result, "output.0", map[string]any{"type": "compaction", "encrypted_content": capsule})
	for _, pair := range [][2]string{{"usage.prompt_tokens", "usage.input_tokens"}, {"usage.completion_tokens", "usage.output_tokens"}, {"usage.total_tokens", "usage.total_tokens"}} {
		if value := gjson.GetBytes(body, pair[0]); value.Exists() {
			result, _ = sjson.SetBytes(result, pair[1], value.Int())
		}
	}
	return cliproxyexecutor.Response{Payload: result, Headers: httpResp.Header.Clone()}, nil
}

func openAICompatFallbackResponseText(body []byte) string {
	content := gjson.GetBytes(body, "choices.0.message.content")
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if !content.IsArray() {
		return ""
	}
	var parts []string
	for _, part := range content.Array() {
		if text := strings.TrimSpace(part.Get("text").String()); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func removeResponsesInputType(payload []byte, itemType string) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload
	}
	items := make([]json.RawMessage, 0, len(input.Array()))
	for _, item := range input.Array() {
		if item.Get("type").String() == itemType {
			continue
		}
		items = append(items, json.RawMessage(item.Raw))
	}
	updated, err := sjson.SetBytes(payload, "input", items)
	if err != nil {
		return payload
	}
	return updated
}

func (e *OpenAICompatExecutor) expandFallbackCompactionCapsules(auth *cliproxyauth.Auth, payload []byte) ([]byte, error) {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload, nil
	}
	items := make([]json.RawMessage, 0, len(input.Array()))
	changed := false
	for _, item := range input.Array() {
		if item.Get("type").String() != "compaction" {
			items = append(items, json.RawMessage(item.Raw))
			continue
		}
		encoded := item.Get("encrypted_content").String()
		if !strings.HasPrefix(encoded, openAICompatFallbackCapsulePrefix) {
			items = append(items, json.RawMessage(item.Raw))
			continue
		}
		capsule, errOpen := e.openFallbackCompactionCapsule(auth, encoded)
		if errOpen != nil {
			return nil, errOpen
		}
		message, _ := json.Marshal(map[string]any{"type": "message", "role": "developer", "content": capsule.Summary})
		items = append(items, message)
		changed = true
	}
	if !changed {
		return payload, nil
	}
	return sjson.SetBytes(payload, "input", items)
}

func (e *OpenAICompatExecutor) sealFallbackCompactionCapsule(auth *cliproxyauth.Auth, model, summary string) (string, error) {
	keys := e.fallbackCompactionKeys(auth)
	if len(keys) == 0 {
		return "", fmt.Errorf("fallback compaction requires a configured API key")
	}
	plain, _ := json.Marshal(openAICompatFallbackCapsule{Provider: e.provider, Model: model, Summary: summary})
	block, errBlock := aes.NewCipher(keys[0])
	if errBlock != nil {
		return "", errBlock
	}
	gcm, errGCM := cipher.NewGCM(block)
	if errGCM != nil {
		return "", errGCM
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, errRead := io.ReadFull(rand.Reader, nonce); errRead != nil {
		return "", errRead
	}
	sealed := gcm.Seal(nonce, nonce, plain, []byte(e.provider))
	return openAICompatFallbackCapsulePrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (e *OpenAICompatExecutor) openFallbackCompactionCapsule(auth *cliproxyauth.Auth, encoded string) (openAICompatFallbackCapsule, error) {
	var capsule openAICompatFallbackCapsule
	raw, errDecode := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, openAICompatFallbackCapsulePrefix))
	if errDecode != nil {
		return capsule, fmt.Errorf("invalid fallback compaction capsule: %w", errDecode)
	}
	for _, key := range e.fallbackCompactionKeys(auth) {
		block, errBlock := aes.NewCipher(key)
		if errBlock != nil {
			continue
		}
		gcm, errGCM := cipher.NewGCM(block)
		if errGCM != nil || len(raw) < gcm.NonceSize() {
			continue
		}
		plain, errOpen := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(e.provider))
		if errOpen != nil || json.Unmarshal(plain, &capsule) != nil {
			continue
		}
		if capsule.Provider != e.provider || strings.TrimSpace(capsule.Summary) == "" {
			continue
		}
		return capsule, nil
	}
	return capsule, fmt.Errorf("fallback compaction capsule cannot be decrypted by this provider")
}

func (e *OpenAICompatExecutor) fallbackCompactionKeys(auth *cliproxyauth.Auth) [][]byte {
	secrets := make([]string, 0)
	if compat := e.resolveCompatConfig(auth); compat != nil {
		for _, entry := range compat.APIKeyEntries {
			if secret := strings.TrimSpace(entry.APIKey); secret != "" {
				secrets = append(secrets, secret)
			}
		}
	}
	if auth != nil && auth.Attributes != nil {
		if secret := strings.TrimSpace(auth.Attributes["api_key"]); secret != "" {
			secrets = append(secrets, secret)
		}
	}
	seen := make(map[[32]byte]struct{})
	keys := make([][]byte, 0, len(secrets))
	for _, secret := range secrets {
		hash := sha256.Sum256([]byte("cpa-openai-compat-compaction\x00" + e.provider + "\x00" + secret))
		if _, ok := seen[hash]; ok {
			continue
		}
		seen[hash] = struct{}{}
		key := make([]byte, len(hash))
		copy(key, hash[:])
		keys = append(keys, key)
	}
	return keys
}

func (e *CodexExecutor) codexFallbackCompactionExecutor(auth *cliproxyauth.Auth) *OpenAICompatExecutor {
	entry := e.resolveCodexConfig(auth)
	if entry == nil || !entry.FallbackCompaction {
		return nil
	}
	_, baseURL := codexCreds(auth)
	baseURL = strings.TrimSpace(baseURL)
	entries := make([]config.OpenAICompatibilityAPIKey, 0)
	for _, candidate := range e.cfg.CodexKey {
		if candidate.FallbackCompaction && strings.EqualFold(strings.TrimSpace(candidate.BaseURL), baseURL) && strings.TrimSpace(candidate.APIKey) != "" {
			entries = append(entries, config.OpenAICompatibilityAPIKey{APIKey: candidate.APIKey})
		}
	}
	compatName := "codex-fallback:" + baseURL
	cfg := *e.cfg
	cfg.OpenAICompatibility = []config.OpenAICompatibility{{Name: compatName, BaseURL: baseURL, FallbackCompaction: true, APIKeyEntries: entries}}
	return NewOpenAICompatExecutor(compatName, &cfg)
}

func codexFallbackCompactionAuth(auth *cliproxyauth.Auth, executor *OpenAICompatExecutor) *cliproxyauth.Auth {
	if auth == nil || executor == nil {
		return auth
	}
	clone := *auth
	clone.Provider = "openai-compatibility"
	clone.Attributes = make(map[string]string, len(auth.Attributes)+1)
	for key, value := range auth.Attributes {
		clone.Attributes[key] = value
	}
	clone.Attributes["compat_name"] = executor.provider
	return &clone
}
