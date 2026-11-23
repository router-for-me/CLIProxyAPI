package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const antigravityCountTokensMaxResponseBytes = 1 << 20

// CountTokens counts tokens for the given request using the Antigravity API.
func (e *AntigravityExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	if antigravityInTOSPause(e.cfg, auth) {
		return cliproxyexecutor.Response{}, newAntigravityTOSPauseStatusErr()
	}

	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("antigravity")
	respCtx := context.WithValue(ctx, "alt", opts.Alt)
	prepared, errPrepare := e.prepareAntigravityRequest(ctx, auth, req, opts, baseModel, false, true)
	if errPrepare != nil {
		return cliproxyexecutor.Response{}, errPrepare
	}
	auth = prepared.auth
	cliproxyauth.NotifyAccessTokenFingerprint(ctx, auth)
	if strings.TrimSpace(prepared.token) == "" {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}

	req.Payload = prepared.originalPayload
	selection := prepared.model
	doc := ojson.NewDoc(prepared.translated)
	if errThinking := helps.ApplyAntigravityRequestThinkingDoc(doc, req, opts, selection); errThinking != nil {
		return cliproxyexecutor.Response{}, errThinking
	}
	e.obfuscateSensitiveWordsDoc(doc)
	helps.PrepareAntigravityClaudeStructuredOutputDoc(selection.ID, doc)
	sanitizeAntigravityGeminiRequestSignaturesDoc(selection.ID, doc)
	if _, errReplay := prepareAntigravityGeminiCountTokensReasoningReplayPayloadDoc(ctx, selection.ID, req, opts, doc); errReplay != nil {
		return cliproxyexecutor.Response{}, errReplay
	}
	ensureAntigravityGeminiLeadingUserContentDoc(selection.ID, doc)

	if selection.MaxOutputTokens > 0 {
		_ = doc.Set("request.model", formatAntigravityCountTokensModel(selection.ID))
		_ = doc.Set("request.generationConfig.maxOutputTokens", selection.MaxOutputTokens)
	} else {
		populateAntigravityCountTokensModelDoc(doc, baseModel)
	}

	_ = doc.Delete("project")
	_ = doc.Delete("model")
	_ = doc.Delete("request.safetySettings")
	_ = doc.Delete("request.toolConfig")
	_ = doc.Delete("request.labels")
	_ = doc.Delete("request.sessionId")
	_ = doc.Delete("request.stream")
	sanitizeAntigravityRequestSchemasForModelIfNeededDoc(selection.ID, doc)

	base := resolveAntigravityRequestBaseURL(auth)
	httpClient := newAntigravityHTTPClient(ctx, e.cfg, auth, 0)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}

	var requestURL strings.Builder
	requestURL.WriteString(base)
	requestURL.WriteString(antigravityCountTokensPath)
	if opts.Alt != "" {
		requestURL.WriteString("?$alt=")
		requestURL.WriteString(url.QueryEscape(opts.Alt))
	}

	ctx = helps.WithPayloadFinalizer(ctx, helps.NewPayloadFinalizerWithSourceModels(e.cfg, e.Identifier(), selection.ID, to.String(), "request", prepared.originalTranslated, req, opts, baseModel))
	helps.FinalizePayloadDoc(ctx, doc)
	payload := helps.SerializeAntigravityPayload(doc, false)
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(payload))
	if errReq != nil {
		return cliproxyexecutor.Response{}, errReq
	}
	// No httpReq.Close: keep the shared Antigravity connection pool usable.
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+prepared.token)
	httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
	if host := resolveHost(base); host != "" {
		httpReq.Host = host
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)

	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       requestURL.String(),
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      payload,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	httpResp, errDo := helps.WithAntigravityHTTPClientTrace(httpClient, auth, "count_tokens").Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return cliproxyexecutor.Response{}, errDo
	}

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	bodyBytes, errRead := io.ReadAll(io.LimitReader(httpResp.Body, antigravityCountTokensMaxResponseBytes+1))
	if errClose := httpResp.Body.Close(); errClose != nil {
		log.Errorf("antigravity executor: close response body error: %v", errClose)
	}
	bodyTruncated := len(bodyBytes) > antigravityCountTokensMaxResponseBytes
	if bodyTruncated {
		bodyBytes = bodyBytes[:antigravityCountTokensMaxResponseBytes]
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, bodyBytes)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return cliproxyexecutor.Response{}, errRead
	}

	statusCode := httpResp.StatusCode
	if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices {
		if bodyTruncated {
			errOversize := statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("antigravity count_tokens response exceeded %d bytes", antigravityCountTokensMaxResponseBytes)}
			helps.RecordAPIResponseError(ctx, e.cfg, errOversize)
			return cliproxyexecutor.Response{}, errOversize
		}
		if errorResult := ojson.GetBytesNoCopy(bodyBytes, "error"); errorResult.Exists() && errorResult.Type != ojson.Null {
			statusCode = antigravityInBandErrorStatusCode(errorResult)
		} else {
			count := ojson.GetBytesNoCopy(bodyBytes, "totalTokens").Int()
			translated := sdktranslator.TranslateTokenCount(respCtx, to, responseFormat, count, bodyBytes)
			return cliproxyexecutor.Response{Payload: translated, Headers: httpResp.Header.Clone()}, nil
		}
	}

	if statusCode == http.StatusTooManyRequests {
		if err429 := e.applyAntigravity429StateWithEvictAll(ctx, auth, selection.ID, false, httpResp.Header, bodyBytes, true); err429 != nil {
			return cliproxyexecutor.Response{}, err429
		}
	}
	switch statusCode {
	case http.StatusForbidden:
		e.applyAntigravityForbiddenState(e.cfg, auth, bodyBytes)
	case http.StatusUnauthorized:
		closeAntigravityAuthIdleTransports(auth)
	}
	return cliproxyexecutor.Response{}, newAntigravityStatusErrWithHeaders(statusCode, httpResp.Header, bodyBytes)
}

// populateAntigravityCountTokensModelDoc sets request.model when absent or blank
// so v1internal:countTokens receives the target tokenizer model after top-level
// model is removed.
func populateAntigravityCountTokensModelDoc(doc *ojson.Doc, baseModel string) {
	if doc == nil {
		return
	}
	if existing := doc.Get("request.model"); existing.Exists() && existing.Type == ojson.String && strings.TrimSpace(existing.String()) != "" {
		return
	}
	candidate := strings.TrimSpace(baseModel)
	if candidate == "" {
		if topModel := doc.Get("model"); topModel.Exists() && topModel.Type == ojson.String {
			candidate = strings.TrimSpace(thinking.ParseSuffix(topModel.String()).ModelName)
		}
	}
	formatted := formatAntigravityCountTokensModel(candidate)
	if formatted == "" {
		return
	}
	_ = doc.Set("request.model", formatted)
}

func formatAntigravityCountTokensModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if strings.HasPrefix(model, "models/") ||
		strings.HasPrefix(model, "projects/") ||
		strings.HasPrefix(model, "publishers/") {
		return model
	}
	return "models/" + model
}
