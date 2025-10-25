package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/wsrelay"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// AistudioExecutor routes AI Studio requests through a websocket-backed transport.
type AistudioExecutor struct {
	provider string
	relay    *wsrelay.Manager
	cfg      *config.Config
}

// NewAistudioExecutor constructs a websocket executor for the provider name.
func NewAistudioExecutor(cfg *config.Config, provider string, relay *wsrelay.Manager) *AistudioExecutor {
	return &AistudioExecutor{provider: strings.ToLower(provider), relay: relay, cfg: cfg}
}

// Identifier returns the provider key served by this executor.
func (e *AistudioExecutor) Identifier() string { return e.provider }

// PrepareRequest is a no-op because websocket transport already injects headers.
func (e *AistudioExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error {
	return nil
}

func (e *AistudioExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	translatedReq, body, err := e.translateRequest(req, opts, false)
	if err != nil {
		return resp, err
	}
	endpoint := e.buildEndpoint(req.Model, body.action, opts.Alt)
	wsReq := &wsrelay.HTTPRequest{
		Method:  http.MethodPost,
		URL:     endpoint,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    body.payload,
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       endpoint,
		Method:    http.MethodPost,
		Headers:   wsReq.Headers.Clone(),
		Body:      bytes.Clone(body.payload),
		Provider:  e.provider,
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	wsResp, err := e.relay.RoundTrip(ctx, e.provider, wsReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	recordAPIResponseMetadata(ctx, e.cfg, wsResp.Status, wsResp.Headers.Clone())
	if len(wsResp.Body) > 0 {
		appendAPIResponseChunk(ctx, e.cfg, bytes.Clone(wsResp.Body))
	}
	if wsResp.Status < 200 || wsResp.Status >= 300 {
		return resp, statusErr{code: wsResp.Status, msg: string(wsResp.Body)}
	}
	reporter.publish(ctx, parseGeminiUsage(wsResp.Body))
	var param any
	out := sdktranslator.TranslateNonStream(ctx, body.toFormat, opts.SourceFormat, req.Model, bytes.Clone(opts.OriginalRequest), bytes.Clone(translatedReq), bytes.Clone(wsResp.Body), &param)
	resp = cliproxyexecutor.Response{Payload: []byte(out)}
	return resp, nil
}

func (e *AistudioExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	translatedReq, body, err := e.translateRequest(req, opts, true)
	if err != nil {
		return nil, err
	}
	endpoint := e.buildEndpoint(req.Model, body.action, opts.Alt)
	wsReq := &wsrelay.HTTPRequest{
		Method:  http.MethodPost,
		URL:     endpoint,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    body.payload,
	}
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       endpoint,
		Method:    http.MethodPost,
		Headers:   wsReq.Headers.Clone(),
		Body:      bytes.Clone(body.payload),
		Provider:  e.provider,
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	wsStream, err := e.relay.Stream(ctx, e.provider, wsReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out
	go func() {
		defer close(out)
		var param any
		metadataLogged := false
		for event := range wsStream {
			if event.Err != nil {
				recordAPIResponseError(ctx, e.cfg, event.Err)
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("wsrelay: %v", event.Err)}
				return
			}
			switch event.Type {
			case wsrelay.MessageTypeStreamStart:
				if !metadataLogged && event.Status > 0 {
					recordAPIResponseMetadata(ctx, e.cfg, event.Status, event.Headers.Clone())
					metadataLogged = true
				}
			case wsrelay.MessageTypeStreamChunk:
				if len(event.Payload) > 0 {
					appendAPIResponseChunk(ctx, e.cfg, bytes.Clone(event.Payload))
					if detail, ok := parseGeminiStreamUsage(event.Payload); ok {
						reporter.publish(ctx, detail)
					}
				}
				lines := sdktranslator.TranslateStream(ctx, body.toFormat, opts.SourceFormat, req.Model, bytes.Clone(opts.OriginalRequest), translatedReq, bytes.Clone(event.Payload), &param)
				for i := range lines {
					out <- cliproxyexecutor.StreamChunk{Payload: []byte(lines[i])}
				}
			case wsrelay.MessageTypeStreamEnd:
				return
			case wsrelay.MessageTypeHTTPResp:
				if !metadataLogged && event.Status > 0 {
					recordAPIResponseMetadata(ctx, e.cfg, event.Status, event.Headers.Clone())
					metadataLogged = true
				}
				if len(event.Payload) > 0 {
					appendAPIResponseChunk(ctx, e.cfg, bytes.Clone(event.Payload))
				}
				lines := sdktranslator.TranslateStream(ctx, body.toFormat, opts.SourceFormat, req.Model, bytes.Clone(opts.OriginalRequest), translatedReq, bytes.Clone(event.Payload), &param)
				for i := range lines {
					out <- cliproxyexecutor.StreamChunk{Payload: []byte(lines[i])}
				}
				reporter.publish(ctx, parseGeminiUsage(event.Payload))
				return
			case wsrelay.MessageTypeError:
				recordAPIResponseError(ctx, e.cfg, event.Err)
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("wsrelay: %v", event.Err)}
				return
			}
		}
	}()
	return stream, nil
}

func (e *AistudioExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	_, body, err := e.translateRequest(req, opts, false)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	endpoint := e.buildEndpoint(req.Model, "countTokens", "")
	wsReq := &wsrelay.HTTPRequest{
		Method:  http.MethodPost,
		URL:     endpoint,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    body.payload,
	}
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       endpoint,
		Method:    http.MethodPost,
		Headers:   wsReq.Headers.Clone(),
		Body:      bytes.Clone(body.payload),
		Provider:  e.provider,
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	resp, err := e.relay.RoundTrip(ctx, e.provider, wsReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return cliproxyexecutor.Response{}, err
	}
	recordAPIResponseMetadata(ctx, e.cfg, resp.Status, resp.Headers.Clone())
	if len(resp.Body) > 0 {
		appendAPIResponseChunk(ctx, e.cfg, bytes.Clone(resp.Body))
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return cliproxyexecutor.Response{}, statusErr{code: resp.Status, msg: string(resp.Body)}
	}
	totalTokens := gjson.GetBytes(resp.Body, "totalTokens").Int()
	if totalTokens <= 0 {
		return cliproxyexecutor.Response{}, fmt.Errorf("wsrelay: totalTokens missing in response")
	}
	translated := sdktranslator.TranslateTokenCount(ctx, body.toFormat, opts.SourceFormat, totalTokens, bytes.Clone(resp.Body))
	return cliproxyexecutor.Response{Payload: []byte(translated)}, nil
}

func (e *AistudioExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	_ = ctx
	return auth, nil
}

type translatedPayload struct {
	payload  []byte
	action   string
	toFormat sdktranslator.Format
}

func (e *AistudioExecutor) translateRequest(req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) ([]byte, translatedPayload, error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini")
	payload := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), stream)
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok {
		payload = util.ApplyGeminiThinkingConfig(payload, budgetOverride, includeOverride)
	}
	payload = disableGeminiThinkingConfig(payload, req.Model)
	payload = fixGeminiImageAspectRatio(req.Model, payload)
	metadataAction := "generateContent"
	if req.Metadata != nil {
		if action, _ := req.Metadata["action"].(string); action == "countTokens" {
			metadataAction = action
		}
	}
	action := metadataAction
	if stream && action != "countTokens" {
		action = "streamGenerateContent"
	}
	payload, _ = sjson.DeleteBytes(payload, "session_id")
	return payload, translatedPayload{payload: payload, action: action, toFormat: to}, nil
}

func (e *AistudioExecutor) buildEndpoint(model, action, alt string) string {
	base := fmt.Sprintf("%s/%s/models/%s:%s", glEndpoint, glAPIVersion, model, action)
	if action == "streamGenerateContent" {
		if alt == "" {
			return base + "?alt=sse"
		}
		return base + "?$alt=" + url.QueryEscape(alt)
	}
	if alt != "" && action != "countTokens" {
		return base + "?$alt=" + url.QueryEscape(alt)
	}
	return base
}
