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

const codexResponsesAggregateIdleTimeout = 10 * time.Minute

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
	statusCode    int
	headers       http.Header
	body          []byte
	completedData []byte
}

func (e *CodexExecutor) prepareCodexRequest(ctx context.Context, from sdktranslator.Format, url string, req cliproxyexecutor.Request, rawJSON []byte) (codexPreparedRequest, error) {
	cache := e.resolvePromptCache(ctx, from, req)
	body := rawJSON
	if cache.ID != "" {
		body = bytes.Clone(rawJSON)
		body, _ = helps.SetJSONBytes(body, "prompt_cache_key", cache.ID)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return codexPreparedRequest{}, err
	}
	if cache.ID != "" {
		httpReq.Header.Set("Session_id", cache.ID)
	}
	// Injection of sticky turn-state / turn-metadata happens later in
	// applyCodexHeaders, which has access to the authenticated identity
	// required to build the logical-session key.
	return codexPreparedRequest{
		httpReq:       httpReq,
		body:          body,
		promptCacheID: cache.ID,
	}, nil
}

// resolvePromptCache decides which prompt_cache_key value to send upstream.
//
// Goals:
//  1. Keep requests belonging to the same logical conversation locked to a
//     single, stable key so upstream prompt caches and our sticky-session
//     state (turn-state / turn-metadata) actually get reused.
//  2. Keep unrelated conversations from the same API key *separated* so the
//     proxy doesn't accidentally stitch independent threads together (which
//     confuses both upstream cache and any server-side routing).
//  3. Preserve the legacy behaviour for clients that supply no conversation
//     hint at all, so existing API-key-only deployments still benefit from
//     some degree of caching.
//
// The precedence mirrors what codex-rs does — it uses the caller-owned
// conversation_id as prompt_cache_key — with additional fallbacks that mine
// conversation-scoped fields out of common client payloads.
func (e *CodexExecutor) resolvePromptCache(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request) helps.CodexCache {
	// Path 1: the caller already supplied a prompt_cache_key. Trust it; this
	// is the codex-rs native path (prompt_cache_key == conversation_id).
	if key := strings.TrimSpace(gjson.GetBytes(req.Payload, "prompt_cache_key").String()); key != "" {
		return helps.CodexCache{ID: key}
	}
	if key := strings.TrimSpace(gjson.GetBytes(req.Payload, "metadata.prompt_cache_key").String()); key != "" {
		return helps.CodexCache{ID: key}
	}

	// Path 2: Claude path retains legacy behaviour (model + user_id) so
	// existing deployments keep warming the same cache entry. We only fall
	// back to the generic fingerprinting logic when user_id is missing.
	if from == "claude" {
		if userID := strings.TrimSpace(gjson.GetBytes(req.Payload, "metadata.user_id").String()); userID != "" {
			key := fmt.Sprintf("%s-%s", req.Model, userID)
			return loadOrCreateCodexCache(key)
		}
	}

	// Path 3/4: derive a conversation fingerprint from whatever
	// conversation-scoped fields the caller happened to include. The
	// fingerprint goes through codexCacheStore, so repeated requests with the
	// same fingerprint map to the same stable UUID even after translator
	// re-renders the payload.
	if fp := conversationFingerprint(req); fp != "" {
		scope := codexPromptCacheScope(ctx)
		key := "fp:" + scope + ":" + req.Model + ":" + fp
		return loadOrCreateCodexCache(key)
	}

	// Path 5 (fallback): api_key-level stable UUID. This is strictly less
	// precise than a real conversation id but preserves backwards-compatible
	// behaviour for callers that send neither prompt_cache_key nor any
	// identifiable content (e.g. the upstream smoke tests that post just
	// {"model": "..."}).
	if from == "openai" {
		if apiKey := strings.TrimSpace(helps.APIKeyFromContext(ctx)); apiKey != "" {
			return helps.CodexCache{
				ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+apiKey)).String(),
			}
		}
	}

	return helps.CodexCache{}
}

// conversationFingerprint extracts a conversation-scoped hint out of req.Payload.
// It intentionally inspects many candidate fields because we serve several
// provider schemas (Claude, OpenAI Chat, OpenAI Responses) and each encodes
// conversation identity differently. Returns an empty string when no stable
// hint can be found.
func conversationFingerprint(req cliproxyexecutor.Request) string {
	payload := req.Payload
	if len(payload) == 0 {
		return ""
	}

	// Prefer explicit conversation identifiers before falling back to a
	// content hash. Order matters: more-specific wins.
	identifierFields := []string{
		"metadata.conversation_id",
		"metadata.conversationId",
		"metadata.thread_id",
		"metadata.threadId",
		"metadata.session_id",
		"metadata.sessionId",
		"conversation_id",
		"conversationId",
		"thread_id",
		"threadId",
		// OpenAI "user" top-level field identifies an end-user; two users on
		// the same api key are almost never the same conversation.
		"user",
	}
	for _, field := range identifierFields {
		if v := strings.TrimSpace(gjson.GetBytes(payload, field).String()); v != "" {
			return "id:" + shortHashString(field+"="+v)
		}
	}

	// Content-derived fingerprint: hash the first user turn. Same first user
	// message + same model ⇒ same conversation, which is the assumption
	// prompt caching is built on anyway.
	if content := firstUserContent(payload); content != "" {
		return "c:" + shortHashString(content)
	}

	return ""
}

// firstUserContent returns a normalized string representation of the first
// user message, looking under the common field names used by the provider
// schemas this proxy accepts.
func firstUserContent(payload []byte) string {
	// OpenAI Chat Completions: messages[*].role == "user"
	if msgs := gjson.GetBytes(payload, "messages"); msgs.IsArray() {
		for _, m := range msgs.Array() {
			if strings.EqualFold(strings.TrimSpace(m.Get("role").String()), "user") {
				if c := strings.TrimSpace(m.Get("content").Raw); c != "" && c != "null" {
					return c
				}
			}
		}
	}
	// OpenAI Responses: input[*].role == "user"
	if inputs := gjson.GetBytes(payload, "input"); inputs.IsArray() {
		for _, m := range inputs.Array() {
			if strings.EqualFold(strings.TrimSpace(m.Get("role").String()), "user") {
				if c := strings.TrimSpace(m.Get("content").Raw); c != "" && c != "null" {
					return c
				}
			}
		}
		// If "input" is a flat array of strings/objects with no explicit role,
		// hash the whole first element.
		if first := inputs.Array(); len(first) > 0 {
			if c := strings.TrimSpace(first[0].Raw); c != "" && c != "null" {
				return c
			}
		}
	}
	// Anthropic Messages API: messages[*].role == "user"; same field name as
	// OpenAI chat so the first branch already handles it. Fall back to
	// top-level "prompt" for older / non-standard clients.
	if p := strings.TrimSpace(gjson.GetBytes(payload, "prompt").Raw); p != "" && p != "null" {
		return p
	}
	return ""
}

// codexPromptCacheScope produces a stable per-caller scope string. Scoping by
// api key (or gin client identity when available) keeps fingerprints from
// colliding across tenants — two different users asking "hello" should not
// share a prompt_cache_key even though their first-user-message hashes match.
func codexPromptCacheScope(ctx context.Context) string {
	if apiKey := strings.TrimSpace(helps.APIKeyFromContext(ctx)); apiKey != "" {
		return "api:" + shortHashString(apiKey)
	}
	return "anon"
}

// loadOrCreateCodexCache returns the cached UUID for key, creating a new
// entry with a 1-hour TTL when absent. Centralising this logic keeps the
// derivation paths consistent and avoids drift between Claude/OpenAI.
func loadOrCreateCodexCache(key string) helps.CodexCache {
	if cache, ok := helps.GetCodexCache(key); ok {
		return cache
	}
	cache := helps.CodexCache{
		ID:     uuid.New().String(),
		Expire: time.Now().Add(time.Hour),
	}
	helps.SetCodexCache(key, cache)
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

		aggregate, errRead := collectCodexResponseAggregateWithIdleTimeout(httpResp.Body, captureBody, codexResponsesAggregateIdleTimeout)
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
		shortHashBytes(prepared.body),
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
	return cloned
}

func collectCodexResponseAggregate(body io.Reader, captureBody bool) (codexNonStreamHTTPResult, error) {
	return collectCodexResponseAggregateWithIdleTimeout(body, captureBody, 0)
}

func collectCodexResponseAggregateWithIdleTimeout(body io.Reader, captureBody bool, idleTimeout time.Duration) (codexNonStreamHTTPResult, error) {
	var idleReader *idleTimeoutReadCloser
	if idleTimeout > 0 {
		if readCloser, ok := body.(io.ReadCloser); ok {
			idleReader = newIdleTimeoutReadCloser(readCloser, idleTimeout)
			body = idleReader
		}
	}
	if idleReader != nil {
		defer idleReader.StopTimer()
	}

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
		eventData, ok := codexEventData(line)
		if !ok {
			return nil
		}
		eventType := codexEventType(eventData)
		switch eventType {
		case "response.incomplete":
			reason := gjson.GetBytes(eventData, "response.incomplete_details.reason").String()
			if reason == "" {
				reason = "unknown"
			}
			log.Warnf("codex aggregate terminated with response.incomplete: reason=%s", reason)
		case "response.failed":
			message := gjson.GetBytes(eventData, "response.error.message").String()
			if message == "" {
				message = "response.failed"
			}
			log.Warnf("codex aggregate terminated with response.failed: %s", message)
		}
		if completed, isCompleted := streamState.processEventDataWithType(eventType, eventData, true); isCompleted {
			result.completedData = completed.data
		}
		return nil
	})
	return result, err
}

type idleTimeoutReadCloser struct {
	io.ReadCloser
	idleTimeout time.Duration
	timer       *time.Timer
}

func newIdleTimeoutReadCloser(body io.ReadCloser, idleTimeout time.Duration) *idleTimeoutReadCloser {
	reader := &idleTimeoutReadCloser{
		ReadCloser:  body,
		idleTimeout: idleTimeout,
	}
	reader.timer = time.AfterFunc(idleTimeout, func() {
		_ = body.Close()
	})
	return reader
}

func (r *idleTimeoutReadCloser) Read(p []byte) (int, error) {
	if r == nil || r.ReadCloser == nil {
		return 0, io.ErrClosedPipe
	}
	n, err := r.ReadCloser.Read(p)
	if err == nil && n > 0 && r.timer != nil {
		r.timer.Reset(r.idleTimeout)
	}
	return n, err
}

func (r *idleTimeoutReadCloser) Close() error {
	if r == nil || r.ReadCloser == nil {
		return nil
	}
	r.StopTimer()
	return r.ReadCloser.Close()
}

func (r *idleTimeoutReadCloser) StopTimer() {
	if r == nil {
		return
	}
	if r.timer != nil {
		r.timer.Stop()
	}
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
	if len(keys) == 0 {
		return "none"
	}

	hasher := sha256.New()
	for _, key := range keys {
		values := append([]string(nil), headers.Values(key)...)
		sort.Strings(values)
		_, _ = hasher.Write([]byte(key))
		_, _ = hasher.Write([]byte{'='})
		for i := range values {
			if i > 0 {
				_, _ = hasher.Write([]byte{0})
			}
			_, _ = hasher.Write([]byte(values[i]))
		}
		_, _ = hasher.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hasher.Sum(nil))[:codexResponseDedupeHashLen]
}

func shortHashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:codexResponseDedupeHashLen]
}

func shortHashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])[:codexResponseDedupeHashLen]
}
