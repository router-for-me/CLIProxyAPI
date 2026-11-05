package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
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

// executeDecisions keeps the Decisions schema out of Responses translation and normalization.
func (e *CodexExecutor) executeDecisions(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if auth == nil || auth.AuthKind() != cliproxyauth.AuthKindAPIKey || strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeAPIKey]) == "" {
		return resp, statusErr{code: http.StatusBadRequest, msg: "OpenAI Decisions requires an API-key credential"}
	}
	if opts.Stream || cliproxyexecutor.ResponseFormatOrSource(opts) != sdktranslator.FormatOpenAIDecisions {
		return resp, statusErr{code: http.StatusBadRequest, msg: "OpenAI Decisions supports only native non-streaming responses"}
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	finalize := helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel, sdktranslator.FormatOpenAIDecisions.String(), "", original, req, opts)
	doc := ojson.NewDoc(req.Payload)
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)
	finalize.FinalizeDoc(doc)
	body := doc.Bytes()

	baseURL := strings.TrimRight(auth.Attributes["base_url"], "/")
	if baseURL == "" {
		baseURL = defaultOpenAISIWCBaseURL
	}
	url := baseURL + "/decisions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+auth.Attributes[cliproxyauth.AttributeAPIKey])
	util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes, opts.Headers)
	applyModelHeaderOverrides(httpReq.Header, baseModel)
	alignCodexContentEncoding(httpReq)
	authType, authValue := auth.AccountInfo()
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL: url, Method: http.MethodPost, Headers: httpReq.Header.Clone(), Body: body,
		Provider: e.Identifier(), AuthID: auth.ID, AuthLabel: auth.Label, AuthType: authType, AuthValue: authValue,
	})
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	httpResp, err := client.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex decisions executor: close response body error: %v", errClose)
		}
	}()
	if errDecode := decodeCodexUpstreamResponse(httpResp); errDecode != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDecode)
		return resp, errDecode
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		status := newOpenAICompatStatusError(httpResp.StatusCode, httpResp.Header, data)
		return resp, &helps.UpstreamResponseError{
			Code: httpResp.StatusCode, Header: httpResp.Header.Clone(), Body: data, RetryDelay: status.retryAfter,
		}
	}
	reporter.SetResponseModel(ojson.GetBytesNoCopy(data, "model").String())
	reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
	return cliproxyexecutor.Response{Payload: data, Headers: httpResp.Header.Clone()}, nil
}
