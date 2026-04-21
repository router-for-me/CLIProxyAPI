package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const codexResponseDedupeHashLen = 16

var codexDedupeIgnoredHeaders = map[string]struct{}{
	"Authorization":                         {},
	"X-Codex-Turn-Metadata":                 {},
	"X-Client-Request-Id":                   {},
	"Traceparent":                           {},
	"Tracestate":                            {},
	"X-Responsesapi-Include-Timing-Metrics": {},
}

type codexPreparedRequest struct {
	httpReq       *http.Request
	body          []byte
	promptCacheID string
}

type codexNonStreamHTTPResult struct {
	statusCode          int
	headers             http.Header
	body                []byte
	completedData       []byte
	outputItemsByIndex  map[int64][]byte
	outputItemsFallback [][]byte
	functionCallsByItem map[string]*codexStreamFunctionCallState
}

func (e *CodexExecutor) prepareCodexRequest(ctx context.Context, from sdktranslator.Format, url string, req cliproxyexecutor.Request, rawJSON []byte) (codexPreparedRequest, error) {
	cache := e.resolvePromptCache(ctx, from, req)
	body := bytes.Clone(rawJSON)
	if cache.ID != "" {
		body, _ = helps.SetJSONBytes(body, "prompt_cache_key", cache.ID)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return codexPreparedRequest{}, err
	}
	if cache.ID != "" {
		httpReq.Header.Set("Session_id", cache.ID)
	}
	return codexPreparedRequest{
		httpReq:       httpReq,
		body:          body,
		promptCacheID: cache.ID,
	}, nil
}

func (e *CodexExecutor) resolvePromptCache(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request) helps.CodexCache {
	var cache helps.CodexCache
	if from == "claude" {
		userIDResult := gjson.GetBytes(req.Payload, "metadata.user_id")
		if userIDResult.Exists() {
			key := fmt.Sprintf("%s-%s", req.Model, userIDResult.String())
			var ok bool
			if cache, ok = helps.GetCodexCache(key); !ok {
				cache = helps.CodexCache{
					ID:     uuid.New().String(),
					Expire: time.Now().Add(time.Hour),
				}
				helps.SetCodexCache(key, cache)
			}
		}
	} else if from == "openai-response" {
		promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key")
		if promptCacheKey.Exists() {
			cache.ID = promptCacheKey.String()
		}
	} else if from == "openai" {
		if apiKey := strings.TrimSpace(helps.APIKeyFromContext(ctx)); apiKey != "" {
			cache.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+apiKey)).String()
		}
	}
	return cache
}

func (e *CodexExecutor) cacheHelper(ctx context.Context, from sdktranslator.Format, url string, req cliproxyexecutor.Request, rawJSON []byte) (*http.Request, error) {
	prepared, err := e.prepareCodexRequest(ctx, from, url, req, rawJSON)
	if err != nil {
		return nil, err
	}
	return prepared.httpReq, nil
}

func (e *CodexExecutor) fetchCodexNonStreamResponse(ctx context.Context, auth *cliproxyauth.Auth, url string, prepared codexPreparedRequest) (codexNonStreamHTTPResult, bool, error) {
	key := e.codexResponseDedupeKey(auth, url, prepared)
	result, executed, shared, err := e.responseDedupe.Do(ctx, key, func() (codexNonStreamHTTPResult, error) {
		httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
		httpResp, errDo := httpClient.Do(prepared.httpReq)
		if errDo != nil {
			return codexNonStreamHTTPResult{}, errDo
		}
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()

		data, errRead := io.ReadAll(httpResp.Body)
		if errRead != nil {
			return codexNonStreamHTTPResult{}, errRead
		}
		return codexNonStreamHTTPResult{
			statusCode: httpResp.StatusCode,
			headers:    httpResp.Header.Clone(),
			body:       data,
		}, nil
	})
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return codexNonStreamHTTPResult{}, executed, err
	}
	if shared && !executed {
		helps.LogWithRequestID(ctx).Debugf("codex executor: deduped non-stream request for %s", url)
	}

	result = result.clone()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, result.statusCode, result.headers)
	helps.AppendAPIResponseChunk(ctx, e.cfg, result.body)
	return result, executed, nil
}

func (e *CodexExecutor) fetchCodexResponsesAggregate(ctx context.Context, auth *cliproxyauth.Auth, url string, prepared codexPreparedRequest) (codexNonStreamHTTPResult, bool, error) {
	key := e.codexResponseDedupeKey(auth, url, prepared)
	captureBody := e.cfg != nil && e.cfg.RequestLog
	result, executed, shared, err := e.responseDedupe.Do(ctx, key, func() (codexNonStreamHTTPResult, error) {
		httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
		httpResp, errDo := httpClient.Do(prepared.httpReq)
		if errDo != nil {
			return codexNonStreamHTTPResult{}, errDo
		}
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()

		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			data, errRead := io.ReadAll(httpResp.Body)
			if errRead != nil {
				return codexNonStreamHTTPResult{}, errRead
			}
			return codexNonStreamHTTPResult{
				statusCode: httpResp.StatusCode,
				headers:    httpResp.Header.Clone(),
				body:       data,
			}, nil
		}

		aggregate, errRead := collectCodexResponseAggregate(httpResp.Body, captureBody)
		if errRead != nil {
			return codexNonStreamHTTPResult{}, errRead
		}
		aggregate.statusCode = httpResp.StatusCode
		aggregate.headers = httpResp.Header.Clone()
		return aggregate, nil
	})
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return codexNonStreamHTTPResult{}, executed, err
	}
	if shared && !executed {
		helps.LogWithRequestID(ctx).Debugf("codex executor: deduped non-stream request for %s", url)
	}

	result = result.clone()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, result.statusCode, result.headers)
	if len(result.body) > 0 {
		helps.AppendAPIResponseChunk(ctx, e.cfg, result.body)
	}
	return result, executed, nil
}

func (e *CodexExecutor) codexResponseDedupeKey(auth *cliproxyauth.Auth, url string, prepared codexPreparedRequest) string {
	if prepared.promptCacheID == "" || len(prepared.body) == 0 {
		return ""
	}
	return strings.Join([]string{
		"codex",
		e.codexResponseDedupeScope(auth),
		http.MethodPost,
		url,
		prepared.promptCacheID,
		shortHashString(string(prepared.body)),
		hashCodexDedupeHeaders(prepared.httpReq.Header),
	}, "|")
}

func (e *CodexExecutor) codexResponseDedupeScope(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return "default"
	}
	if id := strings.TrimSpace(auth.ID); id != "" {
		return "id:" + id
	}

	parts := make([]string, 0, 3)
	if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
		parts = append(parts, "proxy="+proxyURL)
	}
	if auth.Attributes != nil {
		if baseURL := strings.TrimSpace(auth.Attributes["base_url"]); baseURL != "" {
			parts = append(parts, "base="+baseURL)
		}
		if apiKey := strings.TrimSpace(auth.Attributes["api_key"]); apiKey != "" {
			parts = append(parts, "api="+shortHashString(apiKey))
		}
	}
	if len(parts) == 0 {
		return "default"
	}
	return strings.Join(parts, ",")
}

func (result codexNonStreamHTTPResult) clone() codexNonStreamHTTPResult {
	cloned := codexNonStreamHTTPResult{
		statusCode: result.statusCode,
		headers:    result.headers.Clone(),
	}
	if len(result.body) > 0 {
		cloned.body = bytes.Clone(result.body)
	}
	if len(result.completedData) > 0 {
		cloned.completedData = bytes.Clone(result.completedData)
	}
	if len(result.outputItemsByIndex) > 0 {
		cloned.outputItemsByIndex = make(map[int64][]byte, len(result.outputItemsByIndex))
		for idx, item := range result.outputItemsByIndex {
			cloned.outputItemsByIndex[idx] = bytes.Clone(item)
		}
	}
	if len(result.outputItemsFallback) > 0 {
		cloned.outputItemsFallback = make([][]byte, len(result.outputItemsFallback))
		for i := range result.outputItemsFallback {
			cloned.outputItemsFallback[i] = bytes.Clone(result.outputItemsFallback[i])
		}
	}
	if len(result.functionCallsByItem) > 0 {
		cloned.functionCallsByItem = make(map[string]*codexStreamFunctionCallState, len(result.functionCallsByItem))
		for key, state := range result.functionCallsByItem {
			if state == nil {
				continue
			}
			copied := *state
			cloned.functionCallsByItem[key] = &copied
		}
	}
	return cloned
}

func collectCodexResponseAggregate(body io.Reader, captureBody bool) (codexNonStreamHTTPResult, error) {
	result := codexNonStreamHTTPResult{}
	streamState := newCodexStreamCompletionState()
	if captureBody {
		result.body = make([]byte, 0, 1024)
	}
	err := helps.ReadStreamLines(body, func(line []byte) error {
		if captureBody {
			result.body = append(result.body, line...)
			result.body = append(result.body, '\n')
		}
		if !bytes.HasPrefix(line, dataTag) {
			return nil
		}

		eventData := bytes.TrimSpace(line[len(dataTag):])
		streamState.recordEvent(eventData)
		if gjson.GetBytes(eventData, "type").String() == "response.completed" {
			result.completedData = bytes.Clone(eventData)
		}
		return nil
	})
	result.outputItemsByIndex = streamState.outputItemsByIndex
	result.outputItemsFallback = streamState.outputItemsFallback
	result.functionCallsByItem = streamState.functionCallsByItem
	return result, err
}

func hashCodexDedupeHeaders(headers http.Header) string {
	if len(headers) == 0 {
		return "none"
	}

	keys := make([]string, 0, len(headers))
	for key := range headers {
		canonical := http.CanonicalHeaderKey(key)
		if _, ignored := codexDedupeIgnoredHeaders[canonical]; ignored {
			continue
		}
		keys = append(keys, canonical)
	}
	sort.Strings(keys)

	var builder strings.Builder
	for _, key := range keys {
		values := append([]string(nil), headers.Values(key)...)
		sort.Strings(values)
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(strings.Join(values, "\x00"))
		builder.WriteByte('\n')
	}
	if builder.Len() == 0 {
		return "none"
	}
	return shortHashString(builder.String())
}

func shortHashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:codexResponseDedupeHashLen]
}
