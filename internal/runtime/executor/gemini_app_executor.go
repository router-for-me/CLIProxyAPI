package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/geminiapp"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// GeminiAppExecutor exposes the signed-in Gemini App's default text model.
type GeminiAppExecutor struct {
	cfg         *config.Config
	authManager *cliproxyauth.Manager
}

// NewGeminiAppExecutor uses manager to merge and persist upstream cookie rotations.
func NewGeminiAppExecutor(cfg *config.Config, manager *cliproxyauth.Manager) *GeminiAppExecutor {
	return &GeminiAppExecutor{cfg: cfg, authManager: manager}
}

func (*GeminiAppExecutor) Identifier() string { return geminiapp.Provider }
func (*GeminiAppExecutor) RequestToFormat(cliproxyexecutor.Request, cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatGemini
}

// PrepareRequest only attaches session cookies to the Gemini App origin.
func (*GeminiAppExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil || req.URL == nil || req.URL.Scheme != "https" || req.URL.Host != "gemini.google.com" {
		return fmt.Errorf("gemini-app: credentials may only be sent to https://gemini.google.com")
	}
	cookies, err := geminiAppCookies(auth)
	if err != nil {
		return err
	}
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")
	for _, name := range []string{geminiapp.CookiePSID, geminiapp.CookiePSIDTS} {
		if value := cookies[name]; value != "" {
			req.AddCookie(&http.Cookie{Name: name, Value: value})
		}
	}
	req.Header.Set("Origin", geminiapp.BaseURL)
	req.Header.Set("Referer", geminiapp.BaseURL+"/app")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	return nil
}

func (e *GeminiAppExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("gemini-app: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.Clone(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	cookies, err := geminiAppCookies(auth)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Del("Cookie") // The isolated jar supplies these exactly once.
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	client := geminiapp.NewClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0), cookies)
	defer e.persistCookies(ctx, auth, cookies, client)
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, geminiapp.NewTransportError("HTTP request", err)
	}
	return resp, nil
}

func (*GeminiAppExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	// Google browser sessions are not OAuth refresh tokens. Acquire a fresh page
	// token for each execution and let the user re-import expired browser cookies.
	return auth, nil
}

func (*GeminiAppExecutor) CountTokens(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, helps.NewGeminiAppRequestError(http.StatusNotImplemented, "gemini-app: token counting is not supported")
}

func (e *GeminiAppExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	opts.Stream = false
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	reporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)
	response := helps.NewGeminiAppResponse(req.Model)
	httpResp, translated, err := e.open(ctx, auth, req, opts, reporter)
	if err != nil {
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gemini-app: close generation response body failed (%T)", errClose)
		}
	}()
	text, err := helps.ReadGeminiAppStream(httpResp.Body, func(string) error { return nil })
	if err != nil {
		return resp, err
	}
	var param any
	payload := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatGemini, cliproxyexecutor.ResponseFormatOrSource(opts), req.Model, helps.ApplyPatchOriginalRequest(req, opts), translated, response(text, true), &param)
	if len(payload) == 0 {
		return resp, statusErr{code: http.StatusBadGateway, msg: "gemini-app: empty translated response"}
	}
	// The web endpoint does not report token usage; do not fabricate token counts.
	reporter.EnsurePublished(ctx)
	return cliproxyexecutor.Response{Payload: payload}, nil
}

func (e *GeminiAppExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	opts.Stream = true
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	reporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)
	response := helps.NewGeminiAppResponse(req.Model)
	httpResp, translated, err := e.open(ctx, auth, req, opts, reporter)
	if err != nil {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(chunks)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("gemini-app: close generation response body failed (%T)", errClose)
			}
		}()
		var param any
		send := func(payload []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			for _, line := range sdktranslator.TranslateStream(ctx, sdktranslator.FormatGemini, cliproxyexecutor.ResponseFormatOrSource(opts), req.Model, helps.ApplyPatchOriginalRequest(req, opts), translated, payload, &param) {
				select {
				case chunks <- cliproxyexecutor.StreamChunk{Payload: line}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}
		_, streamErr := helps.ReadGeminiAppStream(httpResp.Body, func(delta string) error { return send(response(delta, false)) })
		if streamErr == nil {
			streamErr = send(response("", true))
		}
		if streamErr == nil {
			streamErr = send([]byte("[DONE]"))
		}
		if streamErr != nil {
			reporter.PublishFailure(ctx, streamErr)
			select {
			case chunks <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
			return
		}
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *GeminiAppExecutor) open(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, reporter *helps.UsageReporter) (*http.Response, []byte, error) {
	if opts.Alt == "responses/compact" {
		return nil, nil, helps.NewGeminiAppRequestError(http.StatusNotImplemented, "gemini-app: compact responses are not supported")
	}
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	for _, input := range [][]byte{original, req.Payload} {
		if err := helps.ValidateGeminiAppOutput(input); err != nil {
			return nil, nil, helps.NewGeminiAppRequestError(http.StatusBadRequest, err.Error())
		}
	}
	// Claude-to-Gemini translation does not preserve output_config.format.
	// Inspect both inbound buffers before translation can drop the contract.
	if opts.SourceFormat == sdktranslator.FormatClaude && (gjson.GetBytes(original, "output_config.format").Exists() || gjson.GetBytes(req.Payload, "output_config.format").Exists()) {
		return nil, nil, helps.NewGeminiAppRequestError(http.StatusBadRequest, "gemini-app: structured output is not supported")
	}
	translatedOriginal, translated, err := helps.TranslateRequestPairReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FormatGemini, req.Model, original, req.Payload, opts.Stream, false)
	if err != nil {
		return nil, nil, err
	}
	prompt, err := helps.GeminiAppPrompt(translated)
	if err != nil {
		return nil, nil, helps.NewGeminiAppRequestError(http.StatusBadRequest, err.Error())
	}
	body := helps.GeminiAppPayload(prompt)
	finalize := helps.NewPayloadFinalizer(e.cfg, e.Identifier(), req.Model, e.Identifier(), "", translatedOriginal, req, opts)
	body = finalize(body)
	// Only transport framing follows the final payload barrier.
	wire, err := helps.GeminiAppWirePayload(body)
	if err != nil {
		return nil, nil, helps.NewGeminiAppRequestError(http.StatusBadRequest, err.Error())
	}
	cookies, err := geminiAppCookies(auth)
	if err != nil {
		return nil, nil, err
	}
	client := geminiapp.NewClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0), cookies)
	// Persist generation-header rotations on success or failure, including
	// streaming execution, without retaining transient page tokens.
	defer func() { e.persistCookies(ctx, auth, cookies, client) }()
	// Session acquisition is an upstream attempt, but its response is not a
	// generation token and must not start or satisfy generation TTFT.
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	session, err := geminiapp.FetchSession(ctx, client)
	// Publish session rotations before starting a possibly slow generation so
	// another request can acquire the account with the new cookies immediately.
	auth = e.persistCookies(ctx, auth, cookies, client)
	cookies = geminiapp.SessionCookies(client)
	if err != nil {
		return nil, nil, err
	}
	query := url.Values{"hl": {"en"}, "rt": {"c"}, "_reqid": {strconv.FormatInt(time.Now().UnixMilli()%900000+100000, 10)}}
	if session.Build != "" {
		query.Set("bl", session.Build)
	}
	if session.ID != "" {
		query.Set("f.sid", session.ID)
	}
	form := url.Values{"at": {session.Token}, "f.req": {string(wire)}}
	endpoint := geminiapp.BaseURL + "/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate?" + query.Encode()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	// The jar carries any cookies updated while fetching the session page.
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	httpReq.Header.Set("Origin", geminiapp.BaseURL)
	httpReq.Header.Set("Referer", geminiapp.BaseURL+"/app")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0")
	httpReq.Header.Set("X-Same-Domain", "1")
	if id := gjson.GetBytes(body, "59").String(); id != "" {
		httpReq.Header.Set("x-goog-ext-525005358-jspb", fmt.Sprintf("[%q,1]", id))
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)
	httpResp, err := reporter.TrackHTTPClient(client).Do(httpReq)
	if err != nil {
		return nil, nil, geminiapp.NewTransportError("generation request", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gemini-app: close generation error response body failed (%T)", errClose)
		}
		// Upstream bodies and headers can contain session material.
		status := httpResp.StatusCode
		if status >= 300 && status < 400 {
			status = http.StatusUnauthorized
		}
		return nil, nil, statusErr{code: status, msg: fmt.Sprintf("gemini-app: generation returned HTTP %d", httpResp.StatusCode)}
	}
	return httpResp, translated, nil
}

func (e *GeminiAppExecutor) persistCookies(ctx context.Context, auth *cliproxyauth.Auth, initial map[string]string, client *http.Client) *cliproxyauth.Auth {
	if e.authManager == nil || auth == nil {
		return auth
	}
	cookies := geminiapp.SessionCookies(client)
	if maps.Equal(initial, cookies) {
		return auth
	}
	updated := auth.Clone()
	stored := make(map[string]any, len(cookies))
	for name, value := range cookies {
		stored[name] = value
	}
	updated.Metadata["cookies"] = stored
	// Preserve concurrent metadata edits and replacement credentials. Do not
	// let downstream cancellation discard cookies already rotated by Google.
	if _, errUpdate := e.authManager.UpdatePreparedAuth(context.WithoutCancel(ctx), auth, updated); errUpdate != nil {
		log.Errorf("gemini-app: persist session cookies failed (%T)", errUpdate)
	}
	// Keep this client's own cookie snapshot as the next merge base. Adopting
	// a concurrent replacement here would let a later rotation overwrite it.
	return updated
}

func geminiAppCookies(auth *cliproxyauth.Auth) (map[string]string, error) {
	if auth == nil || auth.Metadata == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "gemini-app: session cookies are missing"}
	}
	data, err := json.Marshal(auth.Metadata["cookies"])
	if err != nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "gemini-app: invalid session cookies"}
	}
	cookies, err := geminiapp.ParseCookies(data)
	if err != nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: err.Error()}
	}
	return cookies, nil
}
