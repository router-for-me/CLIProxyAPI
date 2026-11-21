package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const copilotRouteKey = "copilot_attempt_route"

type CopilotExecutor struct {
	cfg   *config.Config
	turns *helps.CopilotTurnLinks
}

func NewCopilotExecutor(cfg *config.Config) *CopilotExecutor {
	return &CopilotExecutor{cfg: cfg, turns: helps.NewCopilotTurnLinks()}
}

// WithConfig preserves pending tool turns across auth reconciliation and config reload.
func (e CopilotExecutor) WithConfig(cfg *config.Config) *CopilotExecutor {
	e.cfg = cfg
	return &e
}

func (*CopilotExecutor) Identifier() string { return copilot.Provider }

type copilotAttemptRoute struct {
	catalog   *copilot.Catalog
	route     copilot.Route
	expiresAt time.Time
}

func (e *CopilotExecutor) PlanRequestRoute(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) cliproxyexecutor.Options {
	if auth == nil {
		return opts
	}
	opts.Metadata = maps.Clone(opts.Metadata)
	if opts.Metadata == nil {
		opts.Metadata = make(map[string]any, 1)
	}
	opts.Metadata[copilotRouteKey] = e.resolveRoute(auth, req, time.Now())
	return opts
}

func (e *CopilotExecutor) resolveRoute(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, now time.Time) copilotAttemptRoute {
	model := strings.TrimPrefix(thinking.ParseSuffix(req.Model).ModelName, copilot.ModelPrefix)
	if e.cfg != nil && e.cfg.Home.Enabled {
		info, _ := cliproxyauth.ResolvedModelInfo(req)
		route, _ := copilot.HomeRoute(model, info)
		return copilotAttemptRoute{route: route}
	}
	catalog := auth.ActiveCopilotCatalog(now)
	route, _ := catalog.Route(model)
	return copilotAttemptRoute{catalog: catalog, route: route, expiresAt: catalog.ExpiresAt()}
}

func (*CopilotExecutor) RequestToFormat(_ cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	plan, _ := opts.Metadata[copilotRouteKey].(copilotAttemptRoute)
	return plan.route.Format
}

func (e *CopilotExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "copilot: missing auth record"}
	}
	githubToken, _ := auth.Metadata["refresh_token"].(string)
	if githubToken == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "copilot: missing GitHub credential; sign in again"}
	}
	client := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 30*time.Second)
	token, err := copilot.Exchange(ctx, client, githubToken)
	if err != nil {
		return nil, err
	}
	base, err := token.API()
	if err != nil {
		return nil, err
	}
	updated := auth.Clone()
	previousBase, _ := updated.Metadata["base_url"].(string)
	updated.Metadata["access_token"] = token.Value
	updated.Metadata["base_url"] = base
	updated.Metadata["expired"] = time.Unix(token.ExpiresAt, 0).UTC().Format(time.RFC3339)
	// Directory discovery runs in the service-owned Copilot queue, never in
	// the shared credential refresh workers. An origin change revokes old routes.
	if previousBase != base && updated.CopilotCatalog != nil {
		updated.CopilotCatalog = copilot.UnavailableCatalog
	}
	return updated, nil
}

// HttpRequest only sends the inference credential to the account's trusted origin.
func (e *CopilotExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	return e.httpRequest(ctx, auth, req, nil)
}

func (e *CopilotExecutor) httpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request, reporter *helps.UsageReporter) (*http.Response, error) {
	if req == nil || req.URL == nil || auth == nil {
		return nil, fmt.Errorf("copilot: missing request or auth record")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	base, _ := auth.Metadata["base_url"].(string)
	base = strings.TrimRight(base, "/")
	token, _ := auth.Metadata["access_token"].(string)
	if token == "" || !copilot.TrustedAPI(base) {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "copilot: missing usable credentials"}
	}
	if req.URL.Scheme+"://"+req.URL.Host != base || req.URL.User != nil {
		return nil, fmt.Errorf("copilot: refusing to send a credential to a different origin")
	}
	req = req.Clone(ctx)
	req.Host = ""
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := *helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if reporter != nil {
		return reporter.TrackHTTPClient(&client).Do(req)
	}
	return client.Do(req)
}

type copilotPreparedRequest struct {
	responseTranslators *sdktranslator.Registry
	route               copilot.Route
	turn                helps.CopilotTurn
	input               helps.CopilotTurnInput
	capture             *helps.CopilotChatCapture
	responsesCapture    *helps.CopilotResponsesCapture
	messagesCapture     *helps.CopilotMessagesCapture
	original, body      []byte
}

func (e *CopilotExecutor) prepare(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream, countOnly bool) (copilotPreparedRequest, error) {
	if auth == nil {
		return copilotPreparedRequest{}, statusErr{code: http.StatusUnauthorized, msg: "copilot: missing auth record"}
	}
	now := time.Now()
	// Re-resolve against the current allowlist even when the account snapshot is unchanged.
	resolved := e.resolveRoute(auth, req, now)
	route := resolved.route
	if plan, planned := opts.Metadata[copilotRouteKey].(copilotAttemptRoute); planned {
		if plan.route.Endpoint != route.Endpoint {
			return copilotPreparedRequest{}, statusErr{code: http.StatusNotFound, msg: "copilot: model route changed after refresh"}
		}
		if plan.catalog == resolved.catalog && !plan.expiresAt.IsZero() && !now.Before(plan.expiresAt) {
			return copilotPreparedRequest{}, statusErr{code: http.StatusNotFound, msg: "copilot: expired model route"}
		}
	}
	if route.Format == "" {
		return copilotPreparedRequest{}, statusErr{code: http.StatusNotFound, msg: "copilot: no supported endpoint for this model and account"}
	}
	baseModel := route.Info.ID
	from, to := opts.SourceFormat, route.Format
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	requestTranslators := helps.CopilotTranslationRegistry(from, to)
	responseTranslators := helps.CopilotTranslationRegistry(responseFormat, to)
	hasResponse := responseTranslators.HasNonStreamResponseTransformer
	if stream {
		hasResponse = responseTranslators.HasStreamResponseTransformer
	}
	if opts.Alt == "responses/compact" || (from != to && !requestTranslators.HasRequestTransformer(from, to)) || (!countOnly && to != responseFormat && !hasResponse(responseFormat, to)) {
		return copilotPreparedRequest{}, statusErr{code: http.StatusBadRequest, msg: "copilot: unsupported request or response protocol for this model"}
	}
	if (from == sdktranslator.FormatOpenAIResponse || from == sdktranslator.FormatCodex) && to != sdktranslator.FormatOpenAIResponse && ojson.GetBytesNoCopy(req.Payload, "previous_response_id").String() != "" {
		return copilotPreparedRequest{}, statusErr{code: http.StatusBadRequest, msg: "copilot: this model cannot resolve previous_response_id; resend the full conversation history without previous_response_id"}
	}
	original := opts.OriginalRequest
	if len(original) == 0 {
		original = req.Payload
	}
	if to == sdktranslator.FormatOpenAI {
		ctx = context.WithValue(ctx, common.InlineChatToolMediaKey{}, true)
	}
	originalTo, body, updatesChanged, err := helps.TranslateCopilotRequestPair(ctx, requestTranslators, opts.Headers, e.cfg, from, to, baseModel, original, req.Payload, stream)
	if err != nil {
		return copilotPreparedRequest{}, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	doc := ojson.NewDoc(body)
	if errSet := doc.Set("model", baseModel); errSet != nil {
		return copilotPreparedRequest{}, statusErr{code: http.StatusBadRequest, msg: "copilot: invalid request payload"}
	}
	if errThinking := helps.ApplyCopilotThinking(requestTranslators, doc, route, req, opts, stream, updatesChanged); errThinking != nil {
		return copilotPreparedRequest{}, errThinking
	}
	source := ojson.ParseBytes(req.Payload)
	helps.NormalizeCopilotRequest(doc, route, from, source, stream)
	input := helps.ParseCopilotTurnInput(req.Payload, from)
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	canReplay := (to == sdktranslator.FormatOpenAI && from != to || to == sdktranslator.FormatOpenAIResponse && from == sdktranslator.FormatOpenAI) && !helps.CopilotPayloadMayWrite(e.cfg, baseModel, requestedModel, from.String(), to.String(), opts.Headers, "model")
	var restored bool
	var responsesCapture *helps.CopilotResponsesCapture
	if to == sdktranslator.FormatOpenAI {
		input.RelayChatToolMedia(doc)
		input.UniqueChatToolIDs(doc, from == sdktranslator.FormatClaude && baseModel == "kimi-k3")
		if canReplay {
			restored = e.turns.RestoreChatReplay(doc, auth.ID, opts, input)
		}
	}
	if to == sdktranslator.FormatOpenAIResponse {
		helps.NormalizeCopilotSystemPrompt(doc, route, from, source)
		if from == sdktranslator.FormatOpenAI && canReplay {
			_, restored = e.turns.RestoreResponsesReplay(doc, auth.ID, opts, ojson.Result{})
		} else if from == sdktranslator.FormatClaude && !helps.CopilotPayloadMayWrite(e.cfg, baseModel, requestedModel, from.String(), to.String(), opts.Headers, "model", "input", "instructions", "conversation", "previous_response_id") {
			// No later payload rule can retarget this history. Bind the capture
			// to both the client transcript and the restored upstream input.
			responsesCapture, restored = e.turns.RestoreResponsesReplay(doc, auth.ID, opts, source)
			if countOnly || responseFormat != sdktranslator.FormatClaude {
				responsesCapture = nil
			}
		}
	}
	var messagesCapture *helps.CopilotMessagesCapture
	if to == sdktranslator.FormatClaude {
		if from == sdktranslator.FormatOpenAI && !helps.CopilotPayloadMayWrite(e.cfg, baseModel, requestedModel, from.String(), to.String(), opts.Headers, "model", "messages", "system") {
			// Derive the prefix before rolling cache breakpoints. No matching payload rule can rewrite this history.
			messagesCapture, restored = e.turns.PrepareMessagesReplay(doc, source, auth.ID, opts)
			if countOnly || responseFormat != sdktranslator.FormatOpenAI {
				messagesCapture = nil
			}
		}
		if shouldEnsureCacheControlDoc(doc, false, false, req.Payload, original) {
			// Unlike Claude Code, Copilot also marks tools when a system prefix exists.
			injectToolsCacheControlDoc(doc)
			injectSystemCacheControlDoc(doc)
			injectMessagesCacheControlDoc(doc)
		}
		stripPromptCacheOptionsDoc(doc)
	}
	helps.ApplyPayloadConfigWithRequestDoc(e.cfg, baseModel, to.String(), from.String(), "", doc, originalTo, requestedModel, helps.PayloadRequestPath(opts), opts.Headers)
	if restored && (doc.Get("model").Type != ojson.String || doc.Get("model").String() != baseModel) {
		return copilotPreparedRequest{}, statusErr{code: http.StatusBadRequest, msg: "copilot: private replay cannot be sent to a different model"}
	}
	if !countOnly && doc.Get("stream").Bool() != stream {
		return copilotPreparedRequest{}, statusErr{code: http.StatusBadRequest, msg: "copilot: payload stream mode does not match the requested response mode"}
	}
	if !countOnly && !stream && to == sdktranslator.FormatClaude && route.NonStreamingOutput > 0 && doc.Get("max_tokens").Int() > int64(route.NonStreamingOutput) {
		return copilotPreparedRequest{}, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("copilot: max_tokens exceeds this model's non-streaming limit of %d; lower max_tokens or enable streaming", route.NonStreamingOutput)}
	}
	return copilotPreparedRequest{responseTranslators: responseTranslators, route: route, input: input, original: original, body: doc.Bytes(), messagesCapture: messagesCapture, responsesCapture: responsesCapture}, nil
}

func (e *CopilotExecutor) send(ctx context.Context, auth *cliproxyauth.Auth, opts cliproxyexecutor.Options, prepared *copilotPreparedRequest, reporter *helps.UsageReporter) (*http.Response, error) {
	base, _ := auth.Metadata["base_url"].(string)
	if !copilot.TrustedAPI(base) {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "copilot: missing trusted API endpoint"}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+prepared.route.Endpoint, bytes.NewReader(prepared.body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	input := prepared.input
	wire := ojson.ParseBytes(prepared.body)
	if prepared.route.Format == sdktranslator.FormatOpenAI && cliproxyexecutor.ResponseFormatOrSource(opts) != sdktranslator.FormatOpenAI {
		prepared.capture = e.turns.NewChatCapture(auth.ID, opts, wire)
	}
	if prepared.route.Format == sdktranslator.FormatOpenAIResponse && cliproxyexecutor.ResponseFormatOrSource(opts) == sdktranslator.FormatOpenAI {
		prepared.responsesCapture = e.turns.NewResponsesCapture(auth.ID, opts, wire)
	}
	prepared.turn = e.turns.Resolve(auth.ID, input, opts, prepared.route.Format, wire)
	helps.SetCopilotInferenceHeaders(httpReq.Header, prepared.route, wire, input.Initiator, prepared.turn)
	reporter.SetUpstreamModel(prepared.route.Info.ID)
	reporter.SetTranslatedRequestMetadata(prepared.body, prepared.route.Format.String())
	resp, err := e.httpRequest(ctx, auth, httpReq, reporter)
	if err != nil {
		return nil, err
	}
	body, err := helps.DecodeResponseBody(resp.Body, resp.Header)
	if err != nil {
		return nil, statusErr{code: http.StatusBadGateway, msg: "copilot: invalid upstream content encoding"}
	}
	resp.Body = body
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer closeCopilotResponse(resp.Body)
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, newOpenAICompatStatusError(resp.StatusCode, resp.Header, payload)
	}
	return resp, nil
}

func closeCopilotResponse(body io.Closer) {
	if err := body.Close(); err != nil {
		log.Debug("copilot: failed to close upstream response")
	}
}

func (e *CopilotExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	prepared, err := e.prepare(ctx, auth, req, opts, false, true)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	var count int64
	if prepared.route.Format == sdktranslator.FormatClaude {
		count, err = helps.CountClaudeInputTokens(prepared.body)
	} else {
		enc, errTokenizer := helps.TokenizerForModel(prepared.route.Info.ID)
		if errTokenizer != nil {
			return cliproxyexecutor.Response{}, errTokenizer
		}
		if prepared.route.Format == sdktranslator.FormatOpenAIResponse {
			count, err = countCodexInputTokens(enc, prepared.body)
		} else {
			count, err = helps.CountOpenAIChatTokens(enc, prepared.body)
		}
	}
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	var payload []byte
	switch cliproxyexecutor.ResponseFormatOrSource(opts) {
	case sdktranslator.FormatGemini:
		payload = common.GeminiTokenCountJSON(count)
	case sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse:
		payload = common.ClaudeInputTokensJSON(count)
	default:
		payload = helps.BuildOpenAIUsageJSON(count)
	}
	return cliproxyexecutor.Response{Payload: payload, Headers: http.Header{"X-Cpa-Token-Count": {"estimated"}}}, nil
}
