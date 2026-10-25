package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

type KiroExecutor struct{ cfg *config.Config }

func NewKiroExecutor(cfg *config.Config) *KiroExecutor { return &KiroExecutor{cfg: cfg} }
func (e *KiroExecutor) Identifier() string             { return "kiro" }
func (e *KiroExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FromString("kiro")
}

func (e *KiroExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil || auth == nil {
		return fmt.Errorf("kiro: request or credential is missing")
	}
	token := kiroauth.String(auth.Metadata, "access_token")
	if token == "" {
		return statusErr{code: http.StatusUnauthorized, msg: "kiro: access token is missing"}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "CLIProxyAPI/kiro")
	util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	return nil
}

func (e *KiroExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("kiro: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	req = req.Clone(ctx)
	if err := e.PrepareRequest(req, auth); err != nil {
		return nil, err
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(req)
}

func (e *KiroExecutor) buildRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, reporter *helps.UsageReporter) (*http.Request, []byte, error) {
	if auth == nil {
		return nil, nil, fmt.Errorf("kiro: credential is missing")
	}
	if opts.Alt == "responses/compact" {
		return nil, nil, statusErr{code: http.StatusNotImplemented, msg: "kiro: responses/compact is not supported"}
	}
	model := thinking.ParseSuffix(req.Model).ModelName
	reporter.SetUpstreamModel(model)
	translated := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FormatClaude, model, bytes.Clone(req.Payload), false)
	translated, err := helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), "claude", e.Identifier())
	if err != nil {
		return nil, nil, err
	}
	body, err := helps.BuildKiroRequest(translated, model, kiroauth.String(auth.Metadata, "profile_arn"))
	if err != nil {
		return nil, nil, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	original := bytes.Clone(body)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, model, "kiro", opts.SourceFormat.String(), "", body, original, helps.PayloadRequestedModel(opts, req.Model), helps.PayloadRequestPath(opts), opts.Headers)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, kiroauth.RuntimeURL(auth.Metadata)+"/generateAssistantResponse", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	if err = e.PrepareRequest(httpReq, auth); err != nil {
		return nil, nil, err
	}
	httpReq.Header.Set("Accept", "application/vnd.amazon.eventstream")
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{URL: httpReq.URL.String(), Method: httpReq.Method, Headers: httpReq.Header.Clone(), Body: body, Provider: e.Identifier(), AuthID: auth.ID, AuthLabel: auth.Label})
	return httpReq, body, nil
}

func (e *KiroExecutor) openResponse(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request, reporter *helps.UsageReporter) (*http.Response, error) {
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	resp, err := client.Do(req)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer closeKiroResponse(resp)
		body, errRead := io.ReadAll(resp.Body)
		if errRead != nil {
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		return nil, statusErr{code: resp.StatusCode, msg: string(body)}
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "eventstream") {
		closeKiroResponse(resp)
		return nil, statusErr{code: http.StatusBadGateway, msg: "kiro: upstream did not return an AWS event stream"}
	}
	return resp, nil
}

func closeKiroResponse(resp *http.Response) {
	if err := resp.Body.Close(); err != nil {
		log.WithError(err).Debug("kiro: close response failed")
	}
}

func (e *KiroExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (result cliproxyexecutor.Response, err error) {
	reporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)
	httpReq, body, err := e.buildRequest(ctx, auth, req, opts, reporter)
	if err != nil {
		return result, err
	}
	resp, err := e.openResponse(ctx, auth, httpReq, reporter)
	if err != nil {
		return result, err
	}
	defer closeKiroResponse(resp)
	state := helps.NewKiroResponse(req.Model)
	for {
		event, data, errRead := helps.ReadKiroEvent(resp.Body)
		if errRead == io.EOF {
			break
		}
		if errRead != nil {
			return result, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		if event == "assistantResponseEvent" || event == "reasoningContentEvent" || event == "toolUseEvent" {
			reporter.ObserveTokenEvent(true)
		}
		if _, errConsume := state.Consume(event, data); errConsume != nil {
			return result, errConsume
		}
	}
	payload, err := state.JSON()
	if err != nil {
		return result, statusErr{code: http.StatusBadGateway, msg: err.Error()}
	}
	var param any
	payload = sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatOpenAI, cliproxyexecutor.ResponseFormatOrSource(opts), req.Model, helps.ApplyPatchOriginalRequest(req, opts), body, payload, &param)
	if len(payload) == 0 {
		return result, statusErr{code: http.StatusBadGateway, msg: "kiro: response translation failed"}
	}
	reporter.Publish(ctx, state.Usage)
	return cliproxyexecutor.Response{Payload: payload, Headers: resp.Header.Clone()}, nil
}

func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	reporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
	reporter.SetStream(true)
	defer reporter.TrackFailure(ctx, &err)
	httpReq, body, err := e.buildRequest(ctx, auth, req, opts, reporter)
	if err != nil {
		return nil, err
	}
	resp, err := e.openResponse(ctx, auth, httpReq, reporter)
	if err != nil {
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer closeKiroResponse(resp)
		state := helps.NewKiroResponse(req.Model)
		var param any
		responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
		originalRequest := helps.SetBoolIfDifferent(bytes.Clone(helps.ApplyPatchOriginalRequest(req, opts)), "stream", true)
		sendError := func(failure error) {
			reporter.PublishFailureWithDetail(ctx, state.Usage, failure)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: failure}:
			case <-ctx.Done():
			}
		}
		emit := func(chunks [][]byte) bool {
			for _, chunk := range chunks {
				translated := sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, responseFormat, req.Model, originalRequest, body, chunk, &param)
				for _, payload := range translated {
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: payload}:
					case <-ctx.Done():
						return false
					}
				}
				if helps.ApplyPatchTranslationError(param) != nil {
					sendError(statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
					return false
				}
			}
			return true
		}
		for {
			event, data, errRead := helps.ReadKiroEvent(resp.Body)
			if errRead == io.EOF {
				break
			}
			if errRead != nil {
				sendError(errRead)
				return
			}
			helps.AppendAPIResponseChunk(ctx, e.cfg, data)
			chunks, errConsume := state.Consume(event, data)
			if errConsume != nil {
				sendError(errConsume)
				return
			}
			if len(chunks) > 0 {
				reporter.ObserveTokenEvent(true)
			}
			if !emit(chunks) {
				reporter.PublishFailureWithDetail(ctx, state.Usage, ctx.Err())
				return
			}
		}
		chunks, errFinish := state.FinishStream()
		if errFinish != nil {
			sendError(statusErr{code: http.StatusBadGateway, msg: errFinish.Error()})
			return
		}
		if !emit(chunks) {
			reporter.PublishFailureWithDetail(ctx, state.Usage, ctx.Err())
			return
		}
		reporter.Publish(ctx, state.Usage)
	}()
	return &cliproxyexecutor.StreamResult{Headers: resp.Header.Clone(), Chunks: out}, nil
}

func (e *KiroExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("kiro: credential is missing")
	}
	updated := auth.Clone()
	kiroauth.NormalizeMetadata(updated.Metadata)
	if kiroauth.String(updated.Metadata, "refresh_token") == "" {
		return updated, nil
	}
	client := &kiroauth.Client{HTTP: helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 30*time.Second)}
	token, err := client.Refresh(ctx, updated.Metadata)
	if err != nil {
		return nil, err
	}
	kiroauth.ApplyToken(updated.Metadata, token)
	delete(updated.Metadata, "kiro_profile_retry_after")
	return updated, nil
}

func (e *KiroExecutor) ShouldPrepareRequestAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	now := time.Now()
	return kiroauth.TokenNeedsRefresh(auth.Metadata, now) || kiroauth.ProfileNeedsLookup(auth.Metadata, now)
}

func (e *KiroExecutor) PrepareRequestAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("kiro: credential is missing")
	}
	updated := auth
	if kiroauth.TokenNeedsRefresh(auth.Metadata, time.Now()) {
		var errRefresh error
		updated, errRefresh = e.Refresh(ctx, auth)
		if errRefresh != nil || updated == nil {
			return updated, errRefresh
		}
	} else {
		updated = auth.Clone()
		kiroauth.NormalizeMetadata(updated.Metadata)
	}
	if !kiroauth.ProfileNeedsLookup(updated.Metadata, time.Now()) {
		return updated, nil
	}
	client := &kiroauth.Client{HTTP: helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)}
	arn, errResolve := client.ResolveProfileARN(ctx, updated.Metadata)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errResolve != nil || arn == "" {
		updated.Metadata["kiro_profile_retry_after"] = time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
		if errResolve != nil {
			log.WithError(errResolve).Debug("kiro: profile lookup failed; deferring the next attempt")
		}
		return updated, nil
	}
	updated.Metadata["profile_arn"] = arn
	delete(updated.Metadata, "kiro_profile_retry_after")
	return updated, nil
}

func (e *KiroExecutor) CountTokens(ctx context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	body := sdktranslator.TranslateRequest(opts.SourceFormat, sdktranslator.FormatOpenAI, req.Model, req.Payload, false)
	enc, err := helps.TokenizerForModel(req.Model)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	count, err := helps.CountOpenAIChatTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	payload := sdktranslator.TranslateTokenCount(ctx, sdktranslator.FormatOpenAI, cliproxyexecutor.ResponseFormatOrSource(opts), count, helps.BuildOpenAIUsageJSON(count))
	return cliproxyexecutor.Response{Payload: payload}, nil
}
