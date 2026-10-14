package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// xaiSpeechRequestError marks upstream speech failures caused by the request itself,
// such as an unknown voice id answered with 404, so the auth manager neither retries
// them on other credentials nor cools down the credential or model.
type xaiSpeechRequestError struct {
	statusErr
}

func (xaiSpeechRequestError) IsRequestScoped() bool {
	return true
}

// xaiSpeechModelUnavailablePatterns mirror the auth manager's model-support phrases so
// model availability failures keep their credential rotation and model cooldown handling.
var xaiSpeechModelUnavailablePatterns = [...]string{
	"model_not_found",
	"model_not_supported",
	"model is not supported",
	"model is unsupported",
	"model not supported",
	"unsupported model",
	"model is not available",
	"model not available",
	"model is unavailable",
	"model unavailable",
	"not available for your plan",
	"not available for your account",
}

// xaiSpeechModelUnavailable reports whether an upstream error body says the speech model
// itself is missing, unsupported or unavailable, as opposed to a bad request value.
func xaiSpeechModelUnavailable(body []byte) bool {
	if !gjson.ValidBytes(body) {
		return xaiSpeechModelUnavailableText(string(body))
	}
	for _, path := range []string{"code", "error.code", "type", "error.type", "error", "error.message", "message", "detail"} {
		if xaiSpeechModelUnavailableText(gjson.GetBytes(body, path).String()) {
			return true
		}
	}
	return false
}

func xaiSpeechModelUnavailableText(text string) bool {
	lower := strings.ToLower(text)
	for _, pattern := range xaiSpeechModelUnavailablePatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// xaiSpeechStatusErr keeps credential, rate-limit, server and model-availability failures on
// the regular xAI error path. Only other 404s, such as an unknown voice id, are flagged as
// request-scoped. 400/422 stay unwrapped because the auth manager already treats them as
// request faults while still honoring model-support errors.
func xaiSpeechStatusErr(code int, body []byte) error {
	err := xaiStatusErr(code, body)
	if err.code == http.StatusNotFound && !xaiSpeechModelUnavailable(body) {
		return xaiSpeechRequestError{statusErr: err}
	}
	return err
}

func (e *XAIExecutor) executeSpeech(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	model := strings.TrimSpace(gjson.GetBytes(req.Payload, "model").String())
	if model == "" {
		model = strings.TrimSpace(req.Model)
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, model, auth)
	defer reporter.TrackFailure(ctx, &err)

	token, _ := xaiCreds(auth)
	requestURL := xaiSpeechRequestURL(auth)
	logXAIResolvedBaseURL(ctx, strings.TrimSuffix(requestURL, xaiTTSPath))

	// Payload rules target the "openai" protocol, matching xAI image/video and config.example.yaml.
	payload := helps.NewPayloadFinalizer(e.cfg, e.Identifier(), model, "openai", "", req.Payload, req, opts)(req.Payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return resp, err
	}
	applyXAIHeaders(httpReq, auth, token, false, "", opts.Headers)
	// Official TTS returns raw audio. The media header helper asks for JSON.
	httpReq.Header.Set("Accept", "*/*")
	e.recordXAIRequest(ctx, auth, requestURL, httpReq.Header.Clone(), payload)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("xai executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = xaiSpeechStatusErr(httpResp.StatusCode, data)
		return resp, err
	}

	reporter.EnsurePublished(ctx)
	return cliproxyexecutor.Response{Payload: data, Headers: httpResp.Header.Clone()}, nil
}
