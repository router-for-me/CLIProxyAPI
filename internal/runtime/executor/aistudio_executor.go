package executor

import (
	"bytes"
	"context"
	"encoding/json"
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

// AIStudioExecutor routes AI Studio requests through a websocket-backed transport.
type AIStudioExecutor struct {
	provider string
	relay    *wsrelay.Manager
	cfg      *config.Config
}

// NewAIStudioExecutor constructs a websocket executor for the provider name.
func NewAIStudioExecutor(cfg *config.Config, provider string, relay *wsrelay.Manager) *AIStudioExecutor {
	return &AIStudioExecutor{provider: strings.ToLower(provider), relay: relay, cfg: cfg}
}

// Identifier returns the logical provider key for routing.
func (e *AIStudioExecutor) Identifier() string { return "aistudio" }

// PrepareRequest is a no-op because websocket transport already injects headers.
func (e *AIStudioExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error {
	return nil
}

func (e *AIStudioExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
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
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	wsResp, err := e.relay.NonStream(ctx, authID, wsReq)
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
	resp = cliproxyexecutor.Response{Payload: ensureColonSpacedJSON([]byte(out))}
	return resp, nil
}

func (e *AIStudioExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
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
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	wsStream, err := e.relay.Stream(ctx, authID, wsReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	firstEvent, ok := <-wsStream
	if !ok {
		err = fmt.Errorf("wsrelay: stream closed before start")
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	if firstEvent.Status > 0 && firstEvent.Status != http.StatusOK {
		metadataLogged := false
		if firstEvent.Status > 0 {
			recordAPIResponseMetadata(ctx, e.cfg, firstEvent.Status, firstEvent.Headers.Clone())
			metadataLogged = true
		}
		var body bytes.Buffer
		if len(firstEvent.Payload) > 0 {
			appendAPIResponseChunk(ctx, e.cfg, bytes.Clone(firstEvent.Payload))
			body.Write(firstEvent.Payload)
		}
		if firstEvent.Type == wsrelay.MessageTypeStreamEnd {
			return nil, statusErr{code: firstEvent.Status, msg: body.String()}
		}
		for event := range wsStream {
			if event.Err != nil {
				recordAPIResponseError(ctx, e.cfg, event.Err)
				if body.Len() == 0 {
					body.WriteString(event.Err.Error())
				}
				break
			}
			if !metadataLogged && event.Status > 0 {
				recordAPIResponseMetadata(ctx, e.cfg, event.Status, event.Headers.Clone())
				metadataLogged = true
			}
			if len(event.Payload) > 0 {
				appendAPIResponseChunk(ctx, e.cfg, bytes.Clone(event.Payload))
				body.Write(event.Payload)
			}
			if event.Type == wsrelay.MessageTypeStreamEnd {
				break
			}
		}
		return nil, statusErr{code: firstEvent.Status, msg: body.String()}
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out
	go func(first wsrelay.StreamEvent) {
		defer close(out)
		var param any
		metadataLogged := false
		processEvent := func(event wsrelay.StreamEvent) bool {
			if event.Err != nil {
				recordAPIResponseError(ctx, e.cfg, event.Err)
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("wsrelay: %v", event.Err)}
				return false
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
					filtered := FilterSSEUsageMetadata(event.Payload)
					if detail, ok := parseGeminiStreamUsage(filtered); ok {
						reporter.publish(ctx, detail)
					}
					lines := sdktranslator.TranslateStream(ctx, body.toFormat, opts.SourceFormat, req.Model, bytes.Clone(opts.OriginalRequest), translatedReq, bytes.Clone(filtered), &param)
					for i := range lines {
						out <- cliproxyexecutor.StreamChunk{Payload: ensureColonSpacedJSON([]byte(lines[i]))}
					}
					break
				}
			case wsrelay.MessageTypeStreamEnd:
				return false
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
					out <- cliproxyexecutor.StreamChunk{Payload: ensureColonSpacedJSON([]byte(lines[i]))}
				}
				reporter.publish(ctx, parseGeminiUsage(event.Payload))
				return false
			case wsrelay.MessageTypeError:
				recordAPIResponseError(ctx, e.cfg, event.Err)
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("wsrelay: %v", event.Err)}
				return false
			}
			return true
		}
		if !processEvent(first) {
			return
		}
		for event := range wsStream {
			if !processEvent(event) {
				return
			}
		}
	}(firstEvent)
	return stream, nil
}

func (e *AIStudioExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	_, body, err := e.translateRequest(req, opts, false)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	body.payload, _ = sjson.DeleteBytes(body.payload, "generationConfig")
	body.payload, _ = sjson.DeleteBytes(body.payload, "tools")
	body.payload, _ = sjson.DeleteBytes(body.payload, "safetySettings")

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
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	resp, err := e.relay.NonStream(ctx, authID, wsReq)
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

func (e *AIStudioExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	_ = ctx
	return auth, nil
}

type translatedPayload struct {
	payload  []byte
	action   string
	toFormat sdktranslator.Format
}

func (e *AIStudioExecutor) translateRequest(req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) ([]byte, translatedPayload, error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini")
	payload := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), stream)
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		if budgetOverride != nil {
			norm := util.NormalizeThinkingBudget(req.Model, *budgetOverride)
			budgetOverride = &norm
		}
		payload = util.ApplyGeminiThinkingConfig(payload, budgetOverride, includeOverride)
	}
	payload = util.ConvertThinkingLevelToBudget(payload)
	payload = util.StripThinkingConfigIfUnsupported(req.Model, payload)
	payload = fixGeminiImageAspectRatio(req.Model, payload)
	payload = applyPayloadConfig(e.cfg, req.Model, payload)
	payload, _ = sjson.DeleteBytes(payload, "generationConfig.maxOutputTokens")
	payload, _ = sjson.DeleteBytes(payload, "generationConfig.responseMimeType")
	payload, _ = sjson.DeleteBytes(payload, "generationConfig.responseJsonSchema")
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

func (e *AIStudioExecutor) buildEndpoint(model, action, alt string) string {
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

// ensureColonSpacedJSON normalizes JSON objects so that colons are followed by a single space while
// keeping the payload otherwise compact. Non-JSON inputs are returned unchanged.
func ensureColonSpacedJSON(payload []byte) []byte {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return payload
	}

	var decoded any
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return payload
	}

	indented, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		return payload
	}

	compacted := make([]byte, 0, len(indented))
	inString := false
	skipSpace := false

	for i := 0; i < len(indented); i++ {
		ch := indented[i]
		if ch == '"' && (i == 0 || indented[i-1] != '\\') {
			inString = !inString
		}

		if !inString {
			if ch == '\n' || ch == '\r' {
				skipSpace = true
				continue
			}
			if skipSpace {
				if ch == ' ' || ch == '\t' {
					continue
				}
				skipSpace = false
			}
		}

		compacted = append(compacted, ch)
	}

	return compacted
}
