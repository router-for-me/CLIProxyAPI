package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	copilotoauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	copilotDefaultBaseURL          = "https://api.githubcopilot.com"
	copilotUserAgent               = "GitHubCopilotChat/0.26.7"
	copilotEditorVersion           = "vscode/1.0"
	copilotEditorPluginVersion     = "copilot-chat/0.26.7"
	copilotIntent                  = "conversation-panel"
	copilotAPIVersion              = "2025-04-01"
	copilotIntegrationID           = "vscode-chat"
	copilotUserAgentLibraryVersion = "electron-fetch"
)

// CopilotExecutor routes GitHub Copilot chat/completions requests through the upstream Copilot API,
// handling mandatory streaming negotiation, SSE aggregation and token refresh semantics.
type CopilotExecutor struct {
	cfg *config.Config
}

// NewCopilotExecutor constructs a Copilot executor bound to the supplied configuration.
func NewCopilotExecutor(cfg *config.Config) *CopilotExecutor {
	return &CopilotExecutor{cfg: cfg}
}

func (e *CopilotExecutor) Identifier() string { return "copilot" }

func (e *CopilotExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

func (e *CopilotExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	var (
		resp cliproxyexecutor.Response
		err  error
	)

	apiKey, baseCandidates := copilotCreds(auth)
	endpoints := copilotEndpointCandidates(baseCandidates)
	if len(endpoints) == 0 {
		err = statusErr{code: http.StatusBadGateway, msg: "copilot executor: no endpoint candidates"}
		return resp, err
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	var lastErr error

	for idx, endpoint := range endpoints {
		httpReq, original, translated, body, buildErr := e.buildRequest(ctx, opts, endpoint, apiKey)
		if buildErr != nil {
			lastErr = buildErr
			continue
		}

		httpResp, retryable, attemptErr := e.issueCopilotRequest(ctx, httpClient, httpReq, body)
		if attemptErr != nil {
			lastErr = attemptErr
			if !retryable || idx == len(endpoints)-1 {
				err = lastErr
				return resp, err
			}
			continue
		}

		resp, parseErr := e.consumeCopilotResponse(ctx, httpResp, reporter, req.Model, opts.SourceFormat, original, translated)
		if parseErr != nil {
			lastErr = parseErr
			err = lastErr
			return resp, err
		}

		err = nil
		return resp, nil
	}

	if lastErr == nil {
		lastErr = statusErr{code: http.StatusBadGateway, msg: "copilot executor: no endpoint succeeded"}
	}
	err = lastErr
	return resp, err
}

func (e *CopilotExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	apiKey, baseCandidates := copilotCreds(auth)
	endpoints := copilotEndpointCandidates(baseCandidates)
	if len(endpoints) == 0 {
		return nil, statusErr{code: http.StatusBadGateway, msg: "copilot executor: no endpoint candidates"}
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	var (
		httpResp *http.Response
		original []byte
		body     []byte
	)

	for idx, endpoint := range endpoints {
		httpReq, orig, _, reqBody, buildErr := e.buildRequest(ctx, opts, endpoint, apiKey)
		if buildErr != nil {
			err = buildErr
			continue
		}

		resp, retryable, attemptErr := e.issueCopilotRequest(ctx, httpClient, httpReq, reqBody)
		if attemptErr != nil {
			err = attemptErr
			if !retryable || idx == len(endpoints)-1 {
				return nil, err
			}
			continue
		}

		httpResp = resp
		original = orig
		body = reqBody
		break
	}

	if httpResp == nil {
		if err == nil {
			err = statusErr{code: http.StatusBadGateway, msg: "copilot executor: no endpoint succeeded"}
		}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk, 8)
	go e.consumeCopilotStream(ctx, reporter, httpResp, original, body, req, opts, out)

	err = nil
	stream = out
	return stream, nil
}

func (e *CopilotExecutor) issueCopilotRequest(ctx context.Context, httpClient *http.Client, httpReq *http.Request, body []byte) (*http.Response, bool, error) {
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:      httpReq.URL.String(),
		Method:   httpReq.Method,
		Headers:  httpReq.Header.Clone(),
		Body:     body,
		Provider: e.Identifier(),
	})

	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, true, err
	}

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, data)
		retryable := isCopilotRetryableStatus(httpResp.StatusCode)
		_ = httpResp.Body.Close()
		if retryable {
			log.Warnf("copilot executor: upstream %s returned %d, attempting fallback", httpReq.URL.String(), httpResp.StatusCode)
		}
		return nil, retryable, statusErr{code: httpResp.StatusCode, msg: string(data)}
	}
	return httpResp, false, nil
}

func (e *CopilotExecutor) consumeCopilotResponse(ctx context.Context, httpResp *http.Response, reporter *usageReporter, model string, sourceFormat sdktranslator.Format, original, translated []byte) (cliproxyexecutor.Response, error) {
	var resp cliproxyexecutor.Response
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("copilot executor: close response body error: %v", errClose)
		}
	}()

	var (
		param       any
		final       string
		accumulator strings.Builder
		lastChunk   []byte
		lastFinish  string
		lastIndex   int64
		lastCreated int64
		lastModel   string
		lastID      string
		messageRole = "assistant"
		rawFallback bytes.Buffer
	)
	scanner := bufio.NewScanner(httpResp.Body)
	buf := make([]byte, 20_971_520)
	scanner.Buffer(buf, 20_971_520)
	for scanner.Scan() {
		line := bytes.Clone(scanner.Bytes())
		appendAPIResponseChunk(ctx, e.cfg, line)
		if !bytes.HasPrefix(line, dataTag) {
			if len(bytes.TrimSpace(line)) > 0 {
				rawFallback.Write(line)
			}
			continue
		}
		data := bytes.TrimSpace(line[len(dataTag):])
		if bytes.Equal(data, []byte("[DONE]")) {
			break
		}
		if gjson.GetBytes(data, "type").String() == "response.completed" {
			if detail, ok := parseCodexUsage(data); ok {
				reporter.publish(ctx, detail)
			}
			final = sdktranslator.TranslateNonStream(
				ctx,
				sdktranslator.FromString("codex"),
				sourceFormat,
				model,
				bytes.Clone(original),
				bytes.Clone(translated),
				data,
				&param,
			)
			break
		}
		if choices := gjson.GetBytes(data, "choices"); choices.Exists() {
			log.Debugf("copilot executor: received OpenAI-style chunk: %s", data)
			lastChunk = bytes.Clone(data)
			lastID = gjson.GetBytes(data, "id").String()
			lastModel = gjson.GetBytes(data, "model").String()
			lastCreated = gjson.GetBytes(data, "created").Int()
			for _, choice := range choices.Array() {
				if deltaRole := choice.Get("delta.role"); deltaRole.Exists() && strings.TrimSpace(deltaRole.String()) != "" {
					messageRole = deltaRole.String()
				}
				if msgRole := choice.Get("message.role"); msgRole.Exists() && strings.TrimSpace(msgRole.String()) != "" {
					messageRole = msgRole.String()
				}
				if deltaContent := choice.Get("delta.content"); deltaContent.Exists() {
					accumulator.WriteString(deltaContent.String())
				}
				if msgContent := choice.Get("message.content"); msgContent.Exists() {
					accumulator.WriteString(msgContent.String())
				}
				if fr := choice.Get("finish_reason"); fr.Exists() && strings.TrimSpace(fr.String()) != "" {
					lastFinish = fr.String()
				}
				if idx := choice.Get("index"); idx.Exists() {
					lastIndex = idx.Int()
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	if final == "" && accumulator.Len() > 0 {
		if lastID == "" {
			lastID = uuid.NewString()
		}
		if lastCreated == 0 {
			lastCreated = time.Now().Unix()
		}
		if lastModel == "" {
			lastModel = model
		}
		if lastFinish == "" {
			lastFinish = "stop"
		}
		result := map[string]any{
			"id":      lastID,
			"object":  "chat.completion",
			"created": lastCreated,
			"model":   lastModel,
			"choices": []map[string]any{
				{
					"index": lastIndex,
					"message": map[string]any{
						"role":    messageRole,
						"content": accumulator.String(),
					},
					"finish_reason": lastFinish,
				},
			},
		}
		if len(lastChunk) > 0 {
			if usage := gjson.GetBytes(lastChunk, "usage"); usage.Exists() {
				var usagePayload map[string]any
				if err := json.Unmarshal([]byte(usage.Raw), &usagePayload); err == nil {
					result["usage"] = usagePayload
				}
			}
		}
		if _, ok := result["usage"]; !ok {
			var usagePayload map[string]any
			if usagePayload == nil {
				usagePayload = map[string]any{
					"prompt_tokens":     0,
					"completion_tokens": 0,
					"total_tokens":      0,
				}
			}
			result["usage"] = usagePayload
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return resp, err
		}
		resp.Payload = payload
		return resp, nil
	}
	if final == "" && rawFallback.Len() > 0 {
		payload := bytes.TrimSpace(rawFallback.Bytes())
		if json.Valid(payload) {
			resp.Payload = append([]byte(nil), payload...)
			return resp, nil
		}
	}
	if final == "" {
		return resp, statusErr{code: http.StatusRequestTimeout, msg: "stream error: stream disconnected before completion: stream closed before response.completed"}
	}
	resp.Payload = []byte(final)
	return resp, nil
}

func (e *CopilotExecutor) consumeCopilotStream(ctx context.Context, reporter *usageReporter, httpResp *http.Response, original, body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, out chan<- cliproxyexecutor.StreamChunk) {
	defer close(out)
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("copilot executor: close response body error: %v", errClose)
		}
	}()

	scanner := bufio.NewScanner(httpResp.Body)
	buf := make([]byte, 20_971_520)
	scanner.Buffer(buf, 20_971_520)
	var param any
	for scanner.Scan() {
		line := bytes.Clone(scanner.Bytes())
		appendAPIResponseChunk(ctx, e.cfg, line)

		if bytes.HasPrefix(line, dataTag) {
			data := bytes.TrimSpace(line[len(dataTag):])
			if bytes.Equal(data, []byte("[DONE]")) {
				break
			}
			if gjson.GetBytes(data, "type").String() == "response.completed" {
				if detail, ok := parseCodexUsage(data); ok {
					reporter.publish(ctx, detail)
				}
			}
		}

		chunks := sdktranslator.TranslateStream(
			ctx,
			sdktranslator.FromString("codex"),
			opts.SourceFormat,
			req.Model,
			bytes.Clone(original),
			body,
			bytes.Clone(line),
			&param,
		)
		for _, chunk := range chunks {
			out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk)}
		}
	}
	if err := scanner.Err(); err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		reporter.publishFailure(ctx)
		out <- cliproxyexecutor.StreamChunk{Err: err}
	}
}

func (e *CopilotExecutor) buildRequest(ctx context.Context, opts cliproxyexecutor.Options, endpoint, apiKey string) (*http.Request, []byte, []byte, []byte, error) {
	body := enforceCopilotStreamFlag(bytes.Clone(opts.OriginalRequest))
	original := bytes.Clone(opts.OriginalRequest)
	translated := bytes.Clone(opts.OriginalRequest)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if strings.TrimSpace(apiKey) != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("user-agent", copilotUserAgent)
	httpReq.Header.Set("editor-version", copilotEditorVersion)
	httpReq.Header.Set("editor-plugin-version", copilotEditorPluginVersion)
	httpReq.Header.Set("openai-intent", copilotIntent)
	httpReq.Header.Set("x-github-api-version", copilotAPIVersion)
	httpReq.Header.Set("x-request-id", uuid.NewString())
	httpReq.Header.Set("copilot-integration-id", copilotIntegrationID)
	httpReq.Header.Set("x-vscode-user-agent-library-version", copilotUserAgentLibraryVersion)

	if initiator := detectCopilotInitiator(original); initiator != "" {
		httpReq.Header.Set("X-Initiator", initiator)
	}
	if detectCopilotVision(original) {
		httpReq.Header.Set("copilot-vision-request", "true")
	}

	return httpReq, original, translated, body, nil
}

type copilotBaseSource string

const (
	copilotBaseSourceAttributes copilotBaseSource = "attributes"
	copilotBaseSourceMetadata   copilotBaseSource = "metadata"
)

type copilotBaseCandidate struct {
	Base   string
	Source copilotBaseSource
}

func copilotEndpointCandidates(candidates []copilotBaseCandidate) []string {
	seen := make(map[string]struct{}, len(candidates)+1)
	endpoints := make([]string, 0, len(candidates)+1)
	for _, candidate := range candidates {
		base := strings.TrimSpace(candidate.Base)
		if base == "" {
			continue
		}
		endpoint := strings.TrimSuffix(base, "/") + "/chat/completions"
		if _, ok := seen[endpoint]; ok {
			continue
		}
		seen[endpoint] = struct{}{}
		endpoints = append(endpoints, endpoint)
	}

	defaultEndpoint := strings.TrimSuffix(copilotDefaultBaseURL, "/") + "/chat/completions"
	if _, ok := seen[defaultEndpoint]; !ok {
		endpoints = append(endpoints, defaultEndpoint)
	}
	return endpoints
}

func copilotCreds(a *cliproxyauth.Auth) (string, []copilotBaseCandidate) {
	var (
		apiKey     string
		candidates []copilotBaseCandidate
	)
	seen := make(map[string]struct{}, 3)
	addCandidate := func(raw string, source copilotBaseSource) {
		sanitized := sanitizeCopilotBaseURL(raw)
		if sanitized == "" {
			return
		}
		key := strings.ToLower(sanitized)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, copilotBaseCandidate{Base: sanitized, Source: source})
	}

	if a == nil {
		return "", candidates
	}
	if a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["api_key"]); v != "" {
			apiKey = v
		}
		addCandidate(a.Attributes["base_url"], copilotBaseSourceAttributes)
	}
	if a.Metadata != nil {
		if apiKey == "" {
			if v, ok := a.Metadata["access_token"].(string); ok {
				apiKey = strings.TrimSpace(v)
			}
		}
		if v, ok := a.Metadata["base_url"].(string); ok {
			addCandidate(v, copilotBaseSourceMetadata)
		}
	}
	return apiKey, candidates
}

func isCopilotRetryableStatus(status int) bool {
	switch status {
	case http.StatusNotFound, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func (e *CopilotExecutor) CountTokens(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte{}}, fmt.Errorf("not implemented")
}

func (e *CopilotExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "copilot executor: auth is nil"}
	}

	var ghPAT string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["github_access_token"].(string); ok {
			ghPAT = strings.TrimSpace(v)
		}
	}
	if ghPAT == "" {
		return auth, nil
	}

	apiBase := ""
	if e.cfg != nil {
		apiBase = strings.TrimSpace(e.cfg.Copilot.GitHubAPIBaseURL)
	}
	if apiBase == "" {
		apiBase = strings.TrimSuffix(copilotoauth.DefaultGitHubAPIBaseURL, "/")
	}
	url := strings.TrimSuffix(apiBase, "/") + copilotoauth.DefaultCopilotTokenPath

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "token "+ghPAT)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cli-proxy-copilot")
	req.Header.Set("OpenAI-Intent", "copilot-cli-refresh")
	req.Header.Set("Editor-Plugin-Name", "cli-proxy")
	req.Header.Set("Editor-Plugin-Version", "1.0.0")
	req.Header.Set("Editor-Version", "cli/1.0")
	req.Header.Set("X-GitHub-Api-Version", "2023-07-07")
	req.Header.Set("X-Vscode-User-Agent-Library-Version", copilotUserAgentLibraryVersion)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, statusErr{code: resp.StatusCode, msg: string(b)}
	}

	var out struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		RefreshIn int    `json:"refresh_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = out.Token
	auth.Metadata["expires_at"] = out.ExpiresAt
	auth.Metadata["refresh_in"] = out.RefreshIn
	auth.Metadata["expired"] = time.Now().Add(time.Duration(out.RefreshIn) * time.Second).Format(time.RFC3339)
	auth.Metadata["type"] = "copilot"
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
	return auth, nil
}

func sanitizeCopilotBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	raw = strings.TrimRight(raw, "/")
	if strings.HasSuffix(strings.ToLower(raw), "/backend-api/codex") {
		raw = strings.TrimSuffix(raw, "/backend-api/codex")
		raw = strings.TrimRight(raw, "/")
	}
	return raw
}

func enforceCopilotStreamFlag(body []byte) []byte {
	if !gjson.ParseBytes(body).Get("stream").Exists() {
		body, _ = sjson.SetBytes(body, "stream", true)
		return body
	}
	body, _ = sjson.SetBytes(body, "stream", true)
	return body
}

func detectCopilotInitiator(body []byte) string {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return "user"
	}
	for _, msg := range messages.Array() {
		role := strings.ToLower(strings.TrimSpace(msg.Get("role").String()))
		if role == "assistant" || role == "tool" {
			return "agent"
		}
	}
	return "user"
}

func detectCopilotVision(body []byte) bool {
	if gjson.GetBytes(body, "copilot_vision_request").Bool() {
		return true
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return false
	}
	for _, msg := range messages.Array() {
		content := msg.Get("content")
		if !content.Exists() {
			continue
		}
		if content.IsArray() {
			for _, block := range content.Array() {
				if strings.EqualFold(block.Get("type").String(), "image_url") {
					return true
				}
			}
		} else if content.Type == gjson.String {
			if strings.Contains(content.String(), "image_url") {
				return true
			}
		}
	}
	return false
}

// deriveCopilotBaseFromToken extracts proxy endpoint from a Copilot access_token when present.
// Example token fragment: "...;proxy-ep=proxy.individual.githubcopilot.com;..."
// Returns: "https://<proxy-ep>/backend-api/codex" or empty string when not derivable.
func deriveCopilotBaseFromToken(tok string) string {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return ""
	}
	const marker = "proxy-ep="
	if idx := strings.Index(tok, marker); idx >= 0 {
		rest := tok[idx+len(marker):]
		if end := strings.IndexByte(rest, ';'); end >= 0 {
			rest = rest[:end]
		}
		host := strings.TrimSpace(rest)
		if host != "" {
			if !strings.Contains(host, "://") {
				host = "https://" + host
			}
			host = strings.TrimRight(host, "/")
			if !strings.HasSuffix(strings.ToLower(host), "/backend-api/codex") {
				host += "/backend-api/codex"
			}
			return host
		}
	}
	return ""
}
