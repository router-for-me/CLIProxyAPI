package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/MoeForge/moego/ojson"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tiktoken-go/tokenizer"
)

// KiroExecutor handles requests directed to the Kiro CLI runtime API.
type KiroExecutor struct {
	cfg *config.Config
}

// NewKiroExecutor constructs a new Kiro executor.
func NewKiroExecutor(cfg *config.Config) *KiroExecutor {
	return &KiroExecutor{cfg: cfg}
}

// Identifier returns the provider identifier for Kiro.
func (e *KiroExecutor) Identifier() string {
	return "kiro"
}

// SupportsApplyPatch reports whether Kiro supports apply_patch requests.
func (e *KiroExecutor) SupportsApplyPatch() bool {
	return true
}

// RequestToFormat specifies that Kiro expects the OpenAI Responses (Codex) wire format.
func (e *KiroExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatCodex
}

func kiroAuthCredentials(auth *cliproxyauth.Auth) (accessToken, profileArn string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		accessToken = auth.Attributes["access_token"]
		if accessToken == "" {
			accessToken = auth.Attributes["api_key"]
		}
		profileArn = auth.Attributes["profile_arn"]
	}
	if auth.Metadata != nil {
		if accessToken == "" {
			if at, ok := auth.Metadata["access_token"].(string); ok {
				accessToken = at
			}
		}
		if profileArn == "" {
			if pa, ok := auth.Metadata["profile_arn"].(string); ok {
				profileArn = pa
			}
		}
	}
	return strings.TrimSpace(accessToken), strings.TrimSpace(profileArn)
}

func kiroRuntimeURL(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if u := strings.TrimSpace(auth.Attributes["base_url"]); u != "" {
			if strings.HasSuffix(u, "/v1/responses") || strings.HasSuffix(u, "/responses") {
				return u
			}
			return strings.TrimSuffix(u, "/") + "/v1/responses"
		}
	}
	return kiroauth.DefaultRuntimeURL
}

func normalizeKiroModel(model string) string {
	m := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "kiro/")
	switch m {
	case "claude-3-5-sonnet", "claude-3-5-sonnet-20241022", "claude-3-5-sonnet-latest", "claude-sonnet-3.5", "claude-sonnet-4.5":
		return "claude-sonnet-4.5"
	case "claude-3-5-haiku", "claude-3-5-haiku-20241022", "claude-3-5-haiku-latest", "claude-haiku-3.5", "claude-haiku-4.5":
		return "claude-haiku-4.5"
	case "deepseek-chat", "deepseek-coder", "deepseek-v3", "deepseek-3.2":
		return "deepseek-3.2"
	case "minimax-m2.5", "minimax-2.5":
		return "minimax-m2.5"
	case "minimax-m2.1", "minimax-2.1":
		return "minimax-m2.1"
	case "glm-5":
		return "glm-5"
	case "qwen3-coder-next", "qwen-coder":
		return "qwen3-coder-next"
	case "auto":
		return "auto"
	default:
		return m
	}
}

// PrepareRequest injects Kiro authorization headers into the outgoing HTTP request.
func (e *KiroExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	accessToken, profileArn := kiroAuthCredentials(auth)
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	if profileArn != "" {
		req.Header.Set("x-amzn-kiro-profile", profileArn)
	}
	req.Header.Set("x-amzn-kiro-origin", "KIRO_CLI")
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	req.Header.Set("User-Agent", "KiroCLI/3.5.0")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Kiro credentials into the request and executes it.
func (e *KiroExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("kiro executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return nil, errPrepare
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Refresh updates Kiro OAuth credentials using the stored refresh token.
func (e *KiroExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("kiro executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "kiro executor: auth is nil"}
	}
	refreshToken := ""
	if auth.Metadata != nil {
		if rt, ok := auth.Metadata["refresh_token"].(string); ok {
			refreshToken = strings.TrimSpace(rt)
		}
	}
	if refreshToken == "" && auth.Attributes != nil {
		refreshToken = strings.TrimSpace(auth.Attributes["refresh_token"])
	}
	if refreshToken == "" {
		return auth, nil
	}

	svc := kiroauth.NewKiroAuthServiceWithProxyURL(e.cfg, auth.ProxyURL)
	bundle, err := svc.RefreshTokens(ctx, refreshToken)
	if err != nil {
		return nil, err
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["type"] = "kiro"
	auth.Metadata["auth_kind"] = "oauth"
	auth.Metadata["access_token"] = bundle.AccessToken
	if bundle.RefreshToken != "" {
		auth.Metadata["refresh_token"] = bundle.RefreshToken
	}
	if bundle.ProfileArn != "" {
		auth.Metadata["profile_arn"] = bundle.ProfileArn
	}
	if bundle.ExpiresAt != "" {
		auth.Metadata["expires_at"] = bundle.ExpiresAt
		auth.Metadata["expired"] = bundle.ExpiresAt
	}

	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["access_token"] = bundle.AccessToken
	auth.Attributes["api_key"] = bundle.AccessToken
	if bundle.RefreshToken != "" {
		auth.Attributes["refresh_token"] = bundle.RefreshToken
	}
	if bundle.ProfileArn != "" {
		auth.Attributes["profile_arn"] = bundle.ProfileArn
	}

	return auth, nil
}

// CountTokens estimates the number of tokens in a request using O200kBase tokenizer.
func (e *KiroExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	targetModel, body, _, responseFormat, err := e.prepareKiroRequest(ctx, auth, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	enc, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("kiro executor: tokenizer init failed: %w", err)
	}
	ids, _, errEnc := enc.Encode(string(body))
	if errEnc != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("kiro executor: token encoding failed: %w", errEnc)
	}
	count := int64(len(ids))
	usageJSON := fmt.Sprintf(`{"response":{"usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}`, count, count)
	translated := sdktranslator.TranslateTokenCount(ctx, sdktranslator.FormatCodex, responseFormat, count, []byte(usageJSON))
	_ = targetModel
	return cliproxyexecutor.Response{Payload: translated}, nil
}

func (e *KiroExecutor) prepareKiroRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (targetModel string, body []byte, from, responseFormat sdktranslator.Format, err error) {
	from = opts.SourceFormat
	responseFormat = cliproxyexecutor.ResponseFormatOrSource(opts)
	targetModel = normalizeKiroModel(req.Model)

	translated := helps.TranslateRequestWithAPIKeyModelCompatibilityForExecutor(
		ctx,
		opts.Headers,
		e.cfg,
		e.Identifier(),
		from,
		sdktranslator.FormatCodex,
		targetModel,
		req.Payload,
		true,
		false,
	)

	doc := ojson.NewDoc(translated)
	helps.SetStringIfDifferentDoc(doc, "model", targetModel)
	helps.SetBoolIfDifferentDoc(doc, "stream", true)
	helps.SetStringIfDifferentDoc(doc, "origin", "KIRO_CLI")

	if _, profileArn := kiroAuthCredentials(auth); profileArn != "" {
		helps.SetStringIfDifferentDoc(doc, "profileArn", profileArn)
	}

	_ = doc.Delete("include")
	_ = doc.Delete("previous_response_id")
	_ = doc.Delete("prompt_cache_retention")
	_ = doc.Delete("safety_identifier")
	_ = doc.Delete("stream_options")
	_ = doc.Delete("parallel_tool_calls")
	_ = doc.Delete("service_tier")
	_ = doc.Delete("user")
	_ = doc.Delete("n")
	_ = doc.Delete("logit_bias")
	_ = doc.Delete("logprobs")
	_ = doc.Delete("top_logprobs")
	_ = doc.Delete("seed")
	_ = doc.Delete("store")
	_ = doc.Delete("metadata")
	_ = doc.Delete("frequency_penalty")
	_ = doc.Delete("presence_penalty")
	_ = doc.Delete("client_side_tools")
	_ = doc.Delete("truncation")
	_ = doc.Delete("max_output_tokens")

	if inst := doc.Get("instructions"); inst.Exists() && strings.TrimSpace(inst.String()) == "" {
		_ = doc.Delete("instructions")
	}

	// reasoning should be omitted as Kiro upstream rejects reasoning.effort
	_ = doc.Delete("reasoning")

	inputRes := doc.Get("input")
	if inputRes.Exists() && inputRes.IsArray() {
		arr := inputRes.Array()
		for i, item := range arr {
			if item.Get("type").String() == "message" {
				_ = doc.Delete(fmt.Sprintf("input.%d.type", i))
			}
			contentRes := item.Get("content")
			if contentRes.Exists() && contentRes.IsArray() && len(contentRes.Array()) == 1 {
				first := contentRes.Array()[0]
				if first.Get("type").String() == "input_text" {
					text := first.Get("text").String()
					helps.SetStringIfDifferentDoc(doc, fmt.Sprintf("input.%d.content", i), text)
				}
			}
		}
	}

	body = sanitizeKiroPayload(doc.Bytes())
	return targetModel, body, from, responseFormat, nil
}

// ExecuteStream executes a streaming inference request against the Kiro runtime.
func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	targetModel, body, _, responseFormat, err := e.prepareKiroRequest(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, targetModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	url := kiroRuntimeURL(auth)
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errReq != nil {
		return nil, errReq
	}
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return nil, errPrepare
	}

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, errRead := io.ReadAll(httpResp.Body)
		_ = httpResp.Body.Close()
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		return nil, statusErr{code: httpResp.StatusCode, msg: string(data)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("kiro executor: close response body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any

		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)

			chunks := sdktranslator.TranslateStream(
				ctx,
				sdktranslator.FormatCodex,
				responseFormat,
				req.Model,
				req.Payload,
				body,
				line,
				&param,
			)

			for _, chunk := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return
				}
			}
		}

		if scanErr := scanner.Err(); scanErr != nil && scanErr != io.EOF {
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: scanErr}:
			case <-ctx.Done():
			}
		}
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

// Execute executes a non-streaming inference request against the Kiro runtime.
// Since Kiro requires stream=true, this drains the SSE stream and converts it to a standard response.
func (e *KiroExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	targetModel, body, _, responseFormat, err := e.prepareKiroRequest(ctx, auth, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, targetModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	url := kiroRuntimeURL(auth)
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errReq != nil {
		return cliproxyexecutor.Response{}, errReq
	}
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return cliproxyexecutor.Response{}, errPrepare
	}

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return cliproxyexecutor.Response{}, errDo
	}
	defer func() {
		_ = httpResp.Body.Close()
	}()

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, errRead := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return cliproxyexecutor.Response{}, errRead
		}
		return cliproxyexecutor.Response{}, statusErr{code: httpResp.StatusCode, msg: string(data)}
	}

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(nil, 52_428_800)

	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	var completedData []byte

	for scanner.Scan() {
		line := scanner.Bytes()
		helps.AppendAPIResponseChunk(ctx, e.cfg, line)

		if bytes.HasPrefix(line, []byte("data: ")) {
			eventData := bytes.TrimSpace(line[6:])
			eventType := ojson.GetBytesNoCopy(eventData, "type").String()
			switch eventType {
			case "response.output_item.done":
				item := ojson.GetBytesNoCopy(eventData, "item")
				if item.Exists() {
					idx := ojson.GetBytesNoCopy(eventData, "output_index")
					if idx.Exists() {
						outputItemsByIndex[idx.Int()] = []byte(item.Raw)
					} else {
						outputItemsFallback = append(outputItemsFallback, []byte(item.Raw))
					}
				}
			case "response.completed", "response.incomplete":
				completedData = bytes.Clone(eventData)
			}
		}
	}

	if scanErr := scanner.Err(); scanErr != nil && scanErr != io.EOF {
		return cliproxyexecutor.Response{}, scanErr
	}

	if len(completedData) == 0 {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: "kiro: stream ended without completed response"}
	}

	// Patch output if response.output is empty but items were collected
	outputResult := ojson.GetBytesNoCopy(completedData, "response.output")
	if (!outputResult.Exists() || !outputResult.IsArray() || len(outputResult.Array()) == 0) && (len(outputItemsByIndex) > 0 || len(outputItemsFallback) > 0) {
		indexes := make([]int64, 0, len(outputItemsByIndex))
		for idx := range outputItemsByIndex {
			indexes = append(indexes, idx)
		}
		sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })

		var buf bytes.Buffer
		buf.WriteByte('[')
		wrote := false
		for _, idx := range indexes {
			if wrote {
				buf.WriteByte(',')
			}
			buf.Write(outputItemsByIndex[idx])
			wrote = true
		}
		for _, item := range outputItemsFallback {
			if wrote {
				buf.WriteByte(',')
			}
			buf.Write(item)
			wrote = true
		}
		buf.WriteByte(']')

		doc := ojson.NewDoc(completedData)
		_ = doc.SetRaw("response.output", buf.String())
		completedData = doc.Bytes()
	}

	var param any
	out := sdktranslator.TranslateNonStream(
		ctx,
		sdktranslator.FormatCodex,
		responseFormat,
		req.Model,
		req.Payload,
		body,
		completedData,
		&param,
	)

	return cliproxyexecutor.Response{
		Payload: out,
		Headers: httpResp.Header.Clone(),
	}, nil
}
