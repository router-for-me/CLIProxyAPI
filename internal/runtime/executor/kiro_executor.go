/**
 * @file Kiro (Amazon Q) executor implementation
 * @description Optimized executor for Kiro provider with cleaner architecture.
 */

package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/from_ir"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/ir"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/to_ir"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

const (
	kiroAPIURL         = "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse"
	kiroRefreshSkew    = 5 * time.Minute
	kiroRequestTimeout = 120 * time.Second
)

var kiroModelMapping = map[string]string{
	"claude-sonnet-4-5":                  "CLAUDE_SONNET_4_5_20250929_V1_0",
	"claude-sonnet-4-5-20250929":         "CLAUDE_SONNET_4_5_20250929_V1_0",
	"claude-sonnet-4-20250514":           "CLAUDE_SONNET_4_20250514_V1_0",
	"claude-3-7-sonnet-20250219":         "CLAUDE_3_7_SONNET_20250219_V1_0",
	"amazonq-claude-sonnet-4-20250514":   "CLAUDE_SONNET_4_20250514_V1_0",
	"amazonq-claude-3-7-sonnet-20250219": "CLAUDE_3_7_SONNET_20250219_V1_0",
	"claude-4-sonnet":                    "CLAUDE_SONNET_4_20250514_V1_0",
	"claude-opus-4-20250514":             "CLAUDE_OPUS_4_20250514_V1_0",
	"claude-opus-4-5-20251101":           "CLAUDE_OPUS_4_5_20251101_V1_0",
	"claude-3-5-sonnet-20241022":         "CLAUDE_3_5_SONNET_20241022_V1_0",
	"claude-3-5-haiku-20241022":          "CLAUDE_3_5_HAIKU_20241022_V1_0",
}

type KiroExecutor struct {
	cfg *config.Config
}

func NewKiroExecutor(cfg *config.Config) *KiroExecutor {
	return &KiroExecutor{cfg: cfg}
}

func (e *KiroExecutor) Identifier() string { return constant.Kiro }

func (e *KiroExecutor) ensureValidToken(ctx context.Context, auth *coreauth.Auth) (string, *coreauth.Auth, error) {
	if auth == nil {
		return "", nil, fmt.Errorf("kiro: auth is nil")
	}
	token := getMetaString(auth.Metadata, "access_token", "accessToken")
	expiry := parseTokenExpiry(auth.Metadata)

	if token != "" && expiry.After(time.Now().Add(kiroRefreshSkew)) {
		return token, nil, nil
	}

	updatedAuth, err := e.Refresh(ctx, auth)
	if err != nil {
		return "", nil, fmt.Errorf("kiro: token refresh failed: %w", err)
	}
	return getMetaString(updatedAuth.Metadata, "access_token", "accessToken"), updatedAuth, nil
}

func (e *KiroExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	var creds kiro.KiroCredentials
	data, _ := json.Marshal(auth.Metadata)
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	newCreds, err := kiro.RefreshTokens(&creds)
	if err != nil {
		return nil, err
	}
	metaBytes, _ := json.Marshal(newCreds)
	var newMeta map[string]interface{}
	json.Unmarshal(metaBytes, &newMeta)

	updatedAuth := auth.Clone()
	updatedAuth.Metadata = newMeta
	updatedAuth.LastRefreshedAt = time.Now()
	if store, ok := auth.Storage.(*kiro.KiroTokenStorage); ok {
		store.KiroCredentials = newCreds
	}
	return updatedAuth, nil
}

type requestContext struct {
	ctx         context.Context
	auth        *coreauth.Auth
	req         cliproxyexecutor.Request
	token       string
	kiroModelID string
	requestID   string
	irReq       *ir.UnifiedChatRequest
	kiroBody    []byte
}

func (e *KiroExecutor) prepareRequest(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request) (*requestContext, error) {
	rc := &requestContext{ctx: ctx, auth: auth, req: req, requestID: uuid.New().String()[:8]}
	var err error
	rc.token, rc.auth, err = e.ensureValidToken(ctx, auth)
	if err != nil {
		return nil, err
	}
	if rc.auth == nil {
		rc.auth = auth
	}

	rc.kiroModelID = mapModelID(req.Model)
	rc.irReq, err = to_ir.ParseOpenAIRequest([]byte(ir.SanitizeText(string(req.Payload))))
	if err != nil {
		return nil, err
	}
	rc.irReq.Model = rc.kiroModelID
	if arn := getMetaString(rc.auth.Metadata, "profile_arn", "profileArn"); arn != "" {
		if rc.irReq.Metadata == nil {
			rc.irReq.Metadata = make(map[string]any)
		}
		rc.irReq.Metadata["profileArn"] = arn
	}

	rc.kiroBody, err = (&from_ir.KiroProvider{}).ConvertRequest(rc.irReq)
	return rc, err
}

func (e *KiroExecutor) buildHTTPRequest(rc *requestContext) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(rc.ctx, "POST", kiroAPIURL, bytes.NewReader(rc.kiroBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	httpReq.Header.Set("x-amz-user-agent", "aws-sdk-js/1.0.7 KiroIDE-0.1.25 CLIProxyAPI")
	httpReq.Header.Set("amz-sdk-request", "attempt=1; max=1")
	if rc.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+rc.token)
	}
	return httpReq, nil
}

func (e *KiroExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	rc, err := e.prepareRequest(ctx, auth, req)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	httpReq, err := e.buildHTTPRequest(rc)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	client := &http.Client{Timeout: kiroRequestTimeout}
	if proxy := e.cfg.ProxyURL; proxy != "" {
		util.SetProxy(&sdkconfig.SDKConfig{ProxyURL: proxy}, client)
	} else if auth.ProxyURL != "" {
		util.SetProxy(&sdkconfig.SDKConfig{ProxyURL: auth.ProxyURL}, client)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return cliproxyexecutor.Response{}, fmt.Errorf("upstream error %d: %s", resp.StatusCode, string(body))
	}

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/vnd.amazon.eventstream") {
		return e.handleEventStreamResponse(resp.Body, req.Model)
	}
	return e.handleJSONResponse(resp.Body, req.Model)
}

func (e *KiroExecutor) handleEventStreamResponse(body io.ReadCloser, model string) (cliproxyexecutor.Response, error) {
	scanner := bufio.NewScanner(body)
	scanner.Split(splitAWSEventStream)
	state := to_ir.NewKiroStreamState()

	for scanner.Scan() {
		payload, err := parseEventPayload(scanner.Bytes())
		if err == nil {
			state.ProcessChunk(payload)
		}
	}

	msg := &ir.Message{Role: ir.RoleAssistant, ToolCalls: state.ToolCalls}
	if state.AccumulatedContent != "" {
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: state.AccumulatedContent})
	}

	converted, err := from_ir.ToOpenAIChatCompletion([]ir.Message{*msg}, nil, model, "chatcmpl-"+uuid.New().String())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: converted}, nil
}

func (e *KiroExecutor) handleJSONResponse(body io.ReadCloser, model string) (cliproxyexecutor.Response, error) {
	rawData, err := io.ReadAll(body)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	messages, usage, err := to_ir.ParseKiroResponse(rawData)
	if err != nil {
		// Fallback: try parsing as event stream if JSON parse fails (sometimes Kiro returns stream-like JSON)
		// But here we just return error or try to handle it.
		// For now, assume ParseKiroResponse handles valid JSON.
		return cliproxyexecutor.Response{}, err
	}

	converted, err := from_ir.ToOpenAIChatCompletion(messages, usage, model, "chatcmpl-"+uuid.New().String())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: converted}, nil
}

func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	rc, err := e.prepareRequest(ctx, auth, req)
	if err != nil {
		return nil, err
	}
	httpReq, err := e.buildHTTPRequest(rc)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Connection", "keep-alive")

	client := &http.Client{}
	if proxy := e.cfg.ProxyURL; proxy != "" {
		util.SetProxy(&sdkconfig.SDKConfig{ProxyURL: proxy}, client)
	} else if auth.ProxyURL != "" {
		util.SetProxy(&sdkconfig.SDKConfig{ProxyURL: auth.ProxyURL}, client)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("upstream error %d: %s", resp.StatusCode, string(body))
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go e.processStream(resp, req.Model, out)
	return out, nil
}

func (e *KiroExecutor) processStream(resp *http.Response, model string, out chan<- cliproxyexecutor.StreamChunk) {
	defer resp.Body.Close()
	defer close(out)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Split(splitAWSEventStream)
	state := to_ir.NewKiroStreamState()
	messageID := "chatcmpl-" + uuid.New().String()
	idx := 0

	for scanner.Scan() {
		payload, err := parseEventPayload(scanner.Bytes())
		if err != nil {
			continue
		}
		events, _ := state.ProcessChunk(payload)
		for _, ev := range events {
			if chunk, _ := from_ir.ToOpenAIChunk(ev, model, messageID, idx); len(chunk) > 0 {
				out <- cliproxyexecutor.StreamChunk{Payload: chunk}
				idx++
			}
		}
	}

	finish := ir.UnifiedEvent{Type: ir.EventTypeFinish, FinishReason: state.DetermineFinishReason()}
	if chunk, _ := from_ir.ToOpenAIChunk(finish, model, messageID, idx); len(chunk) > 0 {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	out <- cliproxyexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}
}

func (e *KiroExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{"total_tokens": 0}`)}, nil
}

func getMetaString(meta map[string]interface{}, keys ...string) string {
	if meta == nil {
		return ""
	}
	for _, key := range keys {
		if v, ok := meta[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func parseTokenExpiry(meta map[string]interface{}) time.Time {
	if meta == nil {
		return time.Time{}
	}
	for _, key := range []string{"expires_at", "expiresAt"} {
		if exp, ok := meta[key].(string); ok && exp != "" {
			if t, err := time.Parse(time.RFC3339, exp); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

func mapModelID(model string) string {
	if mapped, ok := kiroModelMapping[model]; ok {
		return mapped
	}
	return model
}

func splitAWSEventStream(data []byte, atEOF bool) (int, []byte, error) {
	if len(data) < 4 {
		if atEOF && len(data) > 0 {
			return len(data), nil, nil
		}
		return 0, nil, nil
	}
	totalLen := int(binary.BigEndian.Uint32(data[0:4]))
	if totalLen < 16 || totalLen > 16*1024*1024 {
		return 1, nil, nil
	}
	if len(data) < totalLen {
		if atEOF {
			return len(data), nil, nil
		}
		return 0, nil, nil
	}
	return totalLen, data[:totalLen], nil
}

func parseEventPayload(frame []byte) ([]byte, error) {
	if len(frame) < 16 {
		return nil, fmt.Errorf("short frame")
	}
	if binary.BigEndian.Uint32(frame[8:12]) != crc32.ChecksumIEEE(frame[0:8]) {
		return nil, fmt.Errorf("crc mismatch")
	}
	totalLen := int(binary.BigEndian.Uint32(frame[0:4]))
	headersLen := int(binary.BigEndian.Uint32(frame[4:8]))
	start, end := 12+headersLen, totalLen-4
	if start >= end || end > len(frame) {
		return nil, fmt.Errorf("bounds")
	}
	return frame[start:end], nil
}
