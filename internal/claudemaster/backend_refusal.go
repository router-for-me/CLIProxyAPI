package claudemaster

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// The session must see errors exactly as Anthropic sends them, so the client reacts as it would
// to Anthropic: an upstream error is relayed byte for byte (status, body, the headers Claude Code
// acts on), even when the scheduler retried after it and the pool then refused. Only a refusal
// with no upstream error behind it is synthesized, in Anthropic's error shape, with the type and
// status the client would get from Anthropic for that situation.

type backendRefusalKind int

const (
	backendRefusalUnavailable backendRefusalKind = iota // 529 overloaded_error: retry later (a cooldown, an active handoff)
	backendRefusalQuota                                 // 429 rate_limit_error: every usable subscription is out of weekly quota
	backendRefusalInvalid                               // 400 invalid_request_error: opaque state the pool will not move
	backendRefusalNotFound                              // 404 not_found_error: the bound subscription does not serve the model
	backendRefusalInternal                              // 500 api_error: the pool's own fault
)

// backendRefusalError is a refusal the selector wrote itself: the reason is what the session
// reads, the kind decides the Anthropic error type and status, and the wrapped core error (when
// there is one) is what the scheduler keeps deciding by, so explaining a refusal changes nothing
// about routing.
type backendRefusalError struct {
	kind    backendRefusalKind
	reason  string
	resetAt time.Time // quota: when the earliest subscription resets, for retry-after
	authID  string    // bound-subscription refusals: which subscription, for the remembered upstream error
	relayed bool      // Anthropic answered this request with an error, and the session receives that error as sent
	model   string
	cause   error
}

func (e *backendRefusalError) Error() string { return e.reason }
func (e *backendRefusalError) Unwrap() error { return e.cause }

func backendRefuse(kind backendRefusalKind, reason string) error {
	return &backendRefusalError{kind: kind, reason: reason}
}

func (k backendRefusalKind) status() int {
	switch k {
	case backendRefusalQuota:
		return http.StatusTooManyRequests
	case backendRefusalInvalid:
		return http.StatusBadRequest
	case backendRefusalNotFound:
		return http.StatusNotFound
	case backendRefusalInternal:
		return http.StatusInternalServerError
	}
	return 529
}

func (k backendRefusalKind) errorType() string {
	switch k {
	case backendRefusalQuota:
		return "rate_limit_error"
	case backendRefusalInvalid:
		return "invalid_request_error"
	case backendRefusalNotFound:
		return "not_found_error"
	case backendRefusalInternal:
		return "api_error"
	}
	return "overloaded_error"
}

// backendUpstreamError is one upstream error response, kept as received except that a
// compressed body is decoded once here (and Content-Encoding/Content-Length dropped with it), so
// the proxy can read the error it relays. The native passthrough hands the body over as Anthropic
// sent it, compressed: on 2026-10-10 every logged 404 read "type= message=" and its body prefix was
// noise, 234 bytes of application/json behind a Content-Encoding the proxy never looked at.
type backendUpstreamError struct {
	status      int
	headers     http.Header
	body        []byte
	encoding    string // the Content-Encoding the body arrived with ("" when none)
	decodeError string // why a declared encoding could not be decoded (the body is then kept raw)
}

type backendUpstreamResponseError interface {
	StatusCode() int
	ResponseHeaders() http.Header
	ResponseBody() []byte
}

// noteBackendUpstreamError keeps the latest upstream error response of this request (the last
// one wins: it is the one the scheduler acted on last).
func noteBackendUpstreamError(ctx context.Context, err error) {
	noteBackendUpstreamErrorFor(ctx, "", "", err)
}

func noteBackendUpstreamErrorFor(ctx context.Context, authID, model string, err error) *backendUpstreamError {
	if err == nil {
		return nil
	}
	var upstream backendUpstreamResponseError
	if !errors.As(err, &upstream) || upstream == nil {
		return nil
	}
	status := upstream.StatusCode()
	if status < 400 || status > 599 {
		return nil
	}
	record := backendUpstreamRecord(upstream)
	logBackendUpstreamError(authID, model, record)
	if state := backendRequestAttempt(ctx); state != nil {
		state.mu.Lock()
		state.upstream = record
		state.mu.Unlock()
	}
	return record
}

func backendUpstreamRecord(upstream backendUpstreamResponseError) *backendUpstreamError {
	record := &backendUpstreamError{status: upstream.StatusCode(), headers: upstream.ResponseHeaders().Clone(), body: append([]byte(nil), upstream.ResponseBody()...)}
	if record.headers == nil {
		record.headers = http.Header{}
	}
	record.encoding = strings.Join(record.headers.Values("Content-Encoding"), ",")
	plain, err := backendDecodeBody(record.body, record.encoding)
	switch {
	case err != nil:
		record.decodeError = err.Error()
	case plain != nil:
		record.body = plain
		record.headers.Del("Content-Encoding")
		record.headers.Del("Content-Length")
	}
	return record
}

// backendDecodeBody undoes the Content-Encoding chain Anthropic (through Cloudflare) answers with:
// gzip, deflate (zlib or raw), br, zstd, in any order, as the executors' own decoder does. With no
// header it recognises gzip and zstd by their magic bytes. It returns nil, nil when there was
// nothing to decode, and an error (body untouched) when a declared encoding does not decode.
func backendDecodeBody(body []byte, contentEncoding string) ([]byte, error) {
	encodings := []string{}
	for _, enc := range strings.Split(contentEncoding, ",") {
		if enc = strings.ToLower(strings.TrimSpace(enc)); enc != "" && enc != "identity" {
			encodings = append(encodings, enc)
		}
	}
	if len(encodings) == 0 {
		switch {
		case len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b:
			encodings = []string{"gzip"}
		case len(body) >= 4 && body[0] == 0x28 && body[1] == 0xb5 && body[2] == 0x2f && body[3] == 0xfd:
			encodings = []string{"zstd"}
		default:
			return nil, nil
		}
	}
	const limit = 1 << 20
	out := body
	for i := len(encodings) - 1; i >= 0; i-- {
		var reader io.Reader
		switch encodings[i] {
		case "gzip", "x-gzip":
			gz, err := gzip.NewReader(bytes.NewReader(out))
			if err != nil {
				return nil, fmt.Errorf("gzip: %w", err)
			}
			reader = gz
		case "deflate":
			zr, err := zlib.NewReader(bytes.NewReader(out))
			if err != nil {
				reader = flate.NewReader(bytes.NewReader(out))
			} else {
				reader = zr
			}
		case "br":
			reader = brotli.NewReader(bytes.NewReader(out))
		case "zstd":
			zd, err := zstd.NewReader(bytes.NewReader(out))
			if err != nil {
				return nil, fmt.Errorf("zstd: %w", err)
			}
			defer zd.Close()
			reader = zd
		default:
			return nil, fmt.Errorf("unknown content encoding %q", encodings[i])
		}
		plain, err := io.ReadAll(io.LimitReader(reader, limit+1))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", encodings[i], err)
		}
		if len(plain) > limit {
			return nil, fmt.Errorf("%s: decoded body exceeds %d bytes", encodings[i], limit)
		}
		out = plain
	}
	return out, nil
}

// anthropicError reads the body as Anthropic's own error object ({"type":"error","error":{...}});
// ok is false for anything else: empty, HTML, plain text, another JSON shape.
func (u *backendUpstreamError) anthropicError() (errorType, message string, ok bool) {
	if u == nil {
		return "", "", false
	}
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(u.body, &body); err != nil || body.Error.Type == "" {
		return "", "", false
	}
	return body.Error.Type, body.Error.Message, true
}

// notFoundWithoutAnthropicError: a 404 that is not Anthropic saying "model not found". Anthropic
// names that (not_found_error, the model in the message); a 404 with no such body came from a
// path or an intermediary, or was a transient. Observed 2026-10-09/10: 139 of them in 26 hours
// across all three subscriptions and the API-key backup, on both models, while the same
// subscriptions served the same models 2xx around them.
func (u *backendUpstreamError) notFoundWithoutAnthropicError() bool {
	if u == nil || u.status != http.StatusNotFound {
		return false
	}
	_, _, anthropic := u.anthropicError()
	return !anthropic
}

// logBackendUpstreamError names what Anthropic answered, so a failure can be read from the
// server log: the subscription's runtime id, the model, the status and the error's own type and
// message (the first 200 characters). An error message is not a token, a body or a URL. When the
// body is NOT Anthropic's error object the line also carries what tells a wrong path or an
// intermediary from Anthropic: the content type, request-id, cf-ray and server headers, and the
// first 120 printable characters of the body. Before this every such line read "type= message="
// and said nothing (2026-10-10).
func logBackendUpstreamError(authID, model string, upstream *backendUpstreamError) {
	lg().Warn("upstream error", backendUpstreamLogFields(authID, model, upstream)...)
}

func backendUpstreamLogFields(authID, model string, upstream *backendUpstreamError) []any {
	errorType, message, anthropic := upstream.anthropicError()
	if len(message) > 200 {
		message = message[:200]
	}
	fields := []any{"auth", authID, "model", model, "status", upstream.status, "type", errorType, "message", message}
	if anthropic {
		return fields
	}
	h := upstream.headers
	if h == nil {
		h = http.Header{}
	}
	return append(fields,
		"anthropic_error", false,
		"content_type", h.Get("Content-Type"),
		"content_encoding", upstream.encoding,
		"decode_error", upstream.decodeError,
		"request_id", h.Get("Request-Id"),
		"cf_ray", h.Get("Cf-Ray"),
		"server", h.Get("Server"),
		"body_bytes", len(upstream.body),
		"body", backendPrintablePrefix(upstream.body, 120),
	)
}

func backendPrintablePrefix(body []byte, limit int) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(string(body), "") {
		if b.Len() >= limit {
			break
		}
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			b.WriteByte(' ')
		case unicode.IsPrint(r):
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// notFoundNamesModel: Anthropic's "model not found" names the model in its message. With no
// recorded body (a failure that carried none) the model is assumed, as before.
func (u *backendUpstreamError) notFoundNamesModel(model string) bool {
	if u == nil {
		return true
	}
	_, message, anthropic := u.anthropicError()
	if !anthropic {
		return true
	}
	return model == "" || strings.Contains(message, model) || strings.Contains(strings.ToLower(message), "model")
}

// backendNotFoundRetryDelay is the pause before the one retry of a 404 with no Anthropic error.
var backendNotFoundRetryDelay = 400 * time.Millisecond

// backendRetryNotFound reports whether err is a 404 with no Anthropic error that this request
// may retry once, on the same subscription, and spends that retry. Nothing has reached the
// session at that point (the callers retry only before a stream exists), so the retry is
// invisible except in the log. The first answer is logged as evidence either way.
func backendRetryNotFound(ctx context.Context, authID, model string, err error) bool {
	if err == nil {
		return false
	}
	var upstream backendUpstreamResponseError
	if !errors.As(err, &upstream) || upstream == nil {
		return false
	}
	record := backendUpstreamRecord(upstream)
	if !record.notFoundWithoutAnthropicError() {
		return false
	}
	state := backendRequestAttempt(ctx)
	if state == nil {
		return false
	}
	state.mu.Lock()
	spent := state.notFoundRetried
	state.notFoundRetried = true
	state.mu.Unlock()
	if spent {
		return false
	}
	logBackendUpstreamError(authID, model, record)
	lg().Info("retrying a 404 with no Anthropic error once on the same subscription", "auth", authID, "model", model)
	select {
	case <-time.After(backendNotFoundRetryDelay):
		return true
	case <-ctx.Done():
		return false
	}
}

func backendUpstreamErrorFrom(ctx context.Context) *backendUpstreamError {
	state := backendRequestAttempt(ctx)
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.upstream
}

// backendRecordingExecutor records each upstream error response on the request, whichever
// executor serves it, including one that arrives inside a stream.
type backendRecordingExecutor struct {
	coreauth.ProviderExecutor
	observe func(authID string, record *backendUpstreamError) // sees every upstream error (the backup's health uses it)
}

func withBackendUpstreamRecording(executor coreauth.ProviderExecutor) coreauth.ProviderExecutor {
	return withBackendUpstreamRecordingObserved(executor, nil)
}

func withBackendUpstreamRecordingObserved(executor coreauth.ProviderExecutor, observe func(string, *backendUpstreamError)) coreauth.ProviderExecutor {
	if executor == nil {
		return nil
	}
	if _, already := executor.(*backendRecordingExecutor); already {
		return executor
	}
	return &backendRecordingExecutor{ProviderExecutor: executor, observe: observe}
}

func (e *backendRecordingExecutor) note(ctx context.Context, authID, model string, err error) {
	if record := noteBackendUpstreamErrorFor(ctx, authID, model, err); record != nil && e.observe != nil {
		e.observe(authID, record)
	}
}

func (e *backendRecordingExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	resp, err := e.ProviderExecutor.Execute(ctx, auth, req, opts)
	if backendRetryNotFound(ctx, backendAuthID(auth), req.Model, err) {
		resp, err = e.ProviderExecutor.Execute(ctx, auth, req, opts)
	}
	e.note(ctx, backendAuthID(auth), req.Model, err)
	return resp, err
}

func (e *backendRecordingExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	resp, err := e.ProviderExecutor.CountTokens(ctx, auth, req, opts)
	if backendRetryNotFound(ctx, backendAuthID(auth), req.Model, err) {
		resp, err = e.ProviderExecutor.CountTokens(ctx, auth, req, opts)
	}
	e.note(ctx, backendAuthID(auth), req.Model, err)
	return resp, err
}

func (e *backendRecordingExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	result, err := e.ProviderExecutor.ExecuteStream(ctx, auth, req, opts)
	authID, model := backendAuthID(auth), req.Model
	// A 404 here is the response itself (no stream was opened), so the retry is safe.
	if backendRetryNotFound(ctx, authID, model, err) {
		result, err = e.ProviderExecutor.ExecuteStream(ctx, auth, req, opts)
	}
	e.note(ctx, authID, model, err)
	if err != nil || result == nil || result.Chunks == nil {
		return result, err
	}
	in := result.Chunks
	out := make(chan coreexecutor.StreamChunk)
	go func() {
		defer close(out)
		for chunk := range in {
			if chunk.Err != nil {
				e.note(ctx, authID, model, chunk.Err)
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				return
			}
		}
	}()
	return &coreexecutor.StreamResult{Headers: result.Headers, Chunks: out}, nil
}

func backendAuthID(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.ID
}

// writeBackendFailure is every error the session sees when no upstream body is passed through
// by the caller: the request's recorded upstream error exactly as Anthropic sent it; else the
// selector's refusal in Anthropic's error shape; else the generic line (also in that shape).
// headers writes the response headers the way the calling path does (native or protocol).
func writeBackendFailure(ctx context.Context, w http.ResponseWriter, fallbackStatus int, headers func(http.Header, http.Header)) {
	if upstream := backendUpstreamErrorFrom(ctx); upstream != nil {
		headers(w.Header(), upstream.headers)
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(upstream.status)
		_, _ = w.Write(upstream.body)
		return
	}
	var refusal *backendRefusalError
	if err := backendRefusalErrorFrom(ctx); errors.As(err, &refusal) && refusal != nil {
		if refusal.kind == backendRefusalQuota {
			writeBackendQuotaHeaders(w.Header(), refusal.resetAt)
		}
		w.Header().Set("Claude-Master-Reason", refusal.reason)
		backendAnthropicError(w, refusal.kind.status(), refusal.kind.errorType(), backendRefusalMessage(refusal))
		return
	}
	if reason := backendRefusal(ctx); reason != "" {
		backendAnthropicError(w, fallbackStatus, "api_error", "claude-master: "+reason)
		return
	}
	backendAnthropicError(w, fallbackStatus, "api_error", "Configured inference failed; no fallback to the native master was attempted")
}

// backendRefusalMessage: a quota refusal carries Anthropic's own rate-limit text, so the client
// shows what it shows for Anthropic's 429 (the reason is in the Claude-Master-Reason header);
// every other kind names the reason, because Anthropic's own texts for those vary anyway.
func backendRefusalMessage(refusal *backendRefusalError) string {
	if refusal.kind == backendRefusalQuota {
		return "This request would exceed your account's rate limit. Please try again later."
	}
	return "claude-master: " + refusal.reason
}

// writeBackendQuotaHeaders: the headers Anthropic sends with a weekly-quota 429 (observed
// 2026-10-09), so the client's retry and "resets at" behave as they do for Anthropic.
func writeBackendQuotaHeaders(dst http.Header, resetAt time.Time) {
	dst.Set("X-Should-Retry", "true")
	dst.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
	dst.Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
	dst.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "1.0")
	dst.Set("Anthropic-Ratelimit-Unified-Representative-Claim", "seven_day")
	if resetAt.IsZero() {
		return
	}
	epoch := strconv.FormatInt(resetAt.Unix(), 10)
	dst.Set("Anthropic-Ratelimit-Unified-Reset", epoch)
	dst.Set("Anthropic-Ratelimit-Unified-7d-Reset", epoch)
	if wait := time.Until(resetAt); wait > 0 {
		dst.Set("Retry-After", strconv.FormatInt(int64(wait/time.Second)+1, 10))
	}
}

// writeBackendCancelled answers a request whose context was cancelled (the core reports 499).
// Either the client has gone, and nobody reads this, or the server cancelled it while stopping,
// and the session must retry: Anthropic's 529 overloaded_error with x-should-retry, never a 499
// with "Configured inference failed", which Claude Code does not retry (seen 2026-10-10 05:45).
func writeBackendCancelled(w http.ResponseWriter) {
	w.Header().Set("X-Should-Retry", "true")
	backendAnthropicError(w, 529, "overloaded_error", "claude-master: the request was cancelled on the server (a restart); retry")
}

func backendAnthropicError(w http.ResponseWriter, status int, errorType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":%q,"message":%q}}`, errorType, message)
}

// earliestResetLocked: when the first of the pool's subscriptions gets its weekly quota back.
func (s *backendSeriesSelector) earliestResetLocked() time.Time {
	var earliest time.Time
	for _, authID := range s.authIDs {
		candidates := []time.Time{s.currentQuotaLocked(authID).resetsAt}
		if until, blocked := s.quotaBlockedUntil[authID]; blocked {
			candidates = append(candidates, until)
		}
		for _, at := range candidates {
			if !at.IsZero() && (earliest.IsZero() || at.Before(earliest)) {
				earliest = at
			}
		}
	}
	return earliest
}

func (s *backendSeriesSelector) quotaRefusalLocked(reason string) error {
	return &backendRefusalError{kind: backendRefusalQuota, reason: reason, resetAt: s.earliestResetLocked()}
}

var _ = strings.TrimSpace
