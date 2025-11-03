
func (e *GeminiContextAwareExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	tokenSource, baseTokenData, err := prepareGeminiCLITokenSource(ctx, e.cfg, auth)
	if err != nil {
		return resp, err
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini-cli")
	budgetOverride, includeOverride, hasOverride := util.GeminiThinkingFromMetadata(req.Metadata)
	basePayload := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)
	if hasOverride && util.ModelSupportsThinking(req.Model) {
		if budgetOverride != nil {
			norm := util.NormalizeThinkingBudget(req.Model, *budgetOverride)
			budgetOverride = &norm
		}
		basePayload = util.ApplyGeminiCLIThinkingConfig(basePayload, budgetOverride, includeOverride)
	}
	basePayload = util.StripThinkingConfigIfUnsupported(req.Model, basePayload)
	basePayload = fixGeminiCLIImageAspectRatio(req.Model, basePayload)

	action := "generateContent"
	if req.Metadata != nil {
		if a, _ := req.Metadata["action"].(string); a == "countTokens" {
			action = "countTokens"
		}
	}

	projectID := strings.TrimSpace(stringValue(auth.Metadata, "project_id"))
	models := cliPreviewFallbackOrder(req.Model)
	if len(models) == 0 || models[0] != req.Model {
		models = append([]string{req.Model}, models...)
	}

	httpClient := newHTTPClient(ctx, e.cfg, auth, 0)
	respCtx := context.WithValue(ctx, "alt", opts.Alt)

	var authID, authLabel, authType, authValue string
	authID = auth.ID
	authLabel = auth.Label
	authType, authValue = auth.AccountInfo()

	var lastStatus int
	var lastBody []byte

	for idx, attemptModel := range models {
		payload := append([]byte(nil), basePayload...)
		if action == "countTokens" {
			payload = deleteJSONField(payload, "project")
			payload = deleteJSONField(payload, "model")
		} else {
			payload = setJSONField(payload, "project", projectID)
			payload = setJSONField(payload, "model", attemptModel)
		}

		payload = e.trimContext(payload)

		tok, errTok := tokenSource.Token()
		if errTok != nil {
			err = errTok
			return resp, err
		}
		updateGeminiCLITokenMetadata(auth, baseTokenData, tok)

		url := fmt.Sprintf("%s/%s:%s", codeAssistEndpoint, codeAssistVersion, action)
		if opts.Alt != "" && action != "countTokens" {
			url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
		}

		reqHTTP, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if errReq != nil {
			err = errReq
			return resp, err
		}
		reqHTTP.Header.Set("Content-Type", "application/json")
		reqHTTP.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		applyGeminiCLIHeaders(reqHTTP)
		reqHTTP.Header.Set("Accept", "application/json")
		recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
			URL:       url,
			Method:    http.MethodPost,
			Headers:   reqHTTP.Header.Clone(),
			Body:      payload,
			Provider:  e.Identifier(),
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})

		httpResp, errDo := httpClient.Do(reqHTTP)
		if errDo != nil {
			recordAPIResponseError(ctx, e.cfg, errDo)
			err = errDo
			return resp, err
		}

		data, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gemini cli executor: close response body error: %v", errClose)
		}
		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		if errRead != nil {
			recordAPIResponseError(ctx, e.cfg, errRead)
			err = errRead
			return resp, err
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
			reporter.publish(ctx, parseGeminiCLIUsage(data))
			var param any
			out := sdktranslator.TranslateNonStream(respCtx, to, from, attemptModel, bytes.Clone(opts.OriginalRequest), payload, data, &param)
			resp = cliproxyexecutor.Response{Payload: []byte(out)}
			return resp, nil
		}

		lastStatus = httpResp.StatusCode
		lastBody = append([]byte(nil), data...)
		log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		if httpResp.StatusCode == 429 {
			if idx+1 < len(models) {
				log.Debugf("gemini cli executor: rate limited, retrying with next model: %s", models[idx+1])
			} else {
				log.Debug("gemini cli executor: rate limited, no additional fallback model")
			}
			continue
		}

		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return resp, err
	}

	if len(lastBody) > 0 {
		appendAPIResponseChunk(ctx, e.cfg, lastBody)
	}
	if lastStatus == 0 {
		lastStatus = 429
	}
	err = statusErr{code: lastStatus, msg: string(lastBody)}
	return resp, err
}

func (e *GeminiContextAwareExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	tokenSource, baseTokenData, err := prepareGeminiCLITokenSource(ctx, e.cfg, auth)
	if err != nil {
		return nil, err
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini-cli")
	budgetOverride, includeOverride, hasOverride := util.GeminiThinkingFromMetadata(req.Metadata)
	basePayload := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)
	if hasOverride && util.ModelSupportsThinking(req.Model) {
		if budgetOverride != nil {
			norm := util.NormalizeThinkingBudget(req.Model, *budgetOverride)
			budgetOverride = &norm
		}
		basePayload = util.ApplyGeminiCLIThinkingConfig(basePayload, budgetOverride, includeOverride)
	}
	basePayload = util.StripThinkingConfigIfUnsupported(req.Model, basePayload)
	basePayload = fixGeminiCLIImageAspectRatio(req.Model, basePayload)

	projectID := strings.TrimSpace(stringValue(auth.Metadata, "project_id"))

	models := cliPreviewFallbackOrder(req.Model)
	if len(models) == 0 || models[0] != req.Model {
		models = append([]string{req.Model}, models...)
	}

	httpClient := newHTTPClient(ctx, e.cfg, auth, 0)
	respCtx := context.WithValue(ctx, "alt", opts.Alt)

	var authID, authLabel, authType, authValue string
	authID = auth.ID
	authLabel = auth.Label
	authType, authValue = auth.AccountInfo()

	var lastStatus int
	var lastBody []byte

	for idx, attemptModel := range models {
		payload := append([]byte(nil), basePayload...)
		payload = setJSONField(payload, "project", projectID)
		payload = setJSONField(payload, "model", attemptModel)

		payload = e.trimContext(payload)

		tok, errTok := tokenSource.Token()
		if errTok != nil {
			err = errTok
			return nil, err
		}
		updateGeminiCLITokenMetadata(auth, baseTokenData, tok)

		url := fmt.Sprintf("%s/%s:%s", codeAssistEndpoint, codeAssistVersion, "streamGenerateContent")
		if opts.Alt == "" {
			url = url + "?alt=sse"
		} else {
			url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
		}

		reqHTTP, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if errReq != nil {
			err = errReq
			return nil, err
		}
		reqHTTP.Header.Set("Content-Type", "application/json")
		reqHTTP.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		applyGeminiCLIHeaders(reqHTTP)
		reqHTTP.Header.Set("Accept", "text/event-stream")
		recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
			URL:       url,
			Method:    http.MethodPost,
			Headers:   reqHTTP.Header.Clone(),
			Body:      payload,
			Provider:  e.Identifier(),
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})

		httpResp, errDo := httpClient.Do(reqHTTP)
		if errDo != nil {
			recordAPIResponseError(ctx, e.cfg, errDo)
			err = errDo
			return nil, err
		}
		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			data, errRead := io.ReadAll(httpResp.Body)
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("gemini cli executor: close response body error: %v", errClose)
			}
			if errRead != nil {
				recordAPIResponseError(ctx, e.cfg, errRead)
				err = errRead
				return nil, err
			}
			appendAPIResponseChunk(ctx, e.cfg, data)
			lastStatus = httpResp.StatusCode
			lastBody = append([]byte(nil), data...)
			log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
			if httpResp.StatusCode == 429 {
				if idx+1 < len(models) {
					log.Debugf("gemini cli executor: rate limited, retrying with next model: %s", models[idx+1])
				} else {
					log.Debug("gemini cli executor: rate limited, no additional fallback model")
				}
				continue
			}
			err = statusErr{code: httpResp.StatusCode, msg: string(data)}
			return nil, err
		}

		out := make(chan cliproxyexecutor.StreamChunk)
		stream = out
		go func(resp *http.Response, reqBody []byte, attempt string) {
			defer close(out)
			defer func() {
				if errClose := resp.Body.Close(); errClose != nil {
					log.Errorf("gemini cli executor: close response body error: %v", errClose)
				}
			}()
			if opts.Alt == "" {
				scanner := bufio.NewScanner(resp.Body)
				buf := make([]byte, 20971520)
				scanner.Buffer(buf, 20971520)
				var param any
				for scanner.Scan() {
					line := scanner.Bytes()
					appendAPIResponseChunk(ctx, e.cfg, line)
					if detail, ok := parseGeminiCLIStreamUsage(line); ok {
						reporter.publish(ctx, detail)
					}
					if bytes.HasPrefix(line, dataTag) {
						segments := sdktranslator.TranslateStream(respCtx, to, from, attempt, bytes.Clone(opts.OriginalRequest), reqBody, bytes.Clone(line), &param)
						for i := range segments {
							out <- cliproxyexecutor.StreamChunk{Payload: []byte(segments[i])}
						}
					}
				}

				segments := sdktranslator.TranslateStream(respCtx, to, from, attempt, bytes.Clone(opts.OriginalRequest), reqBody, bytes.Clone([]byte("[DONE]")), &param)
				for i := range segments {
					out <- cliproxyexecutor.StreamChunk{Payload: []byte(segments[i])}
				}
				if errScan := scanner.Err(); errScan != nil {
					recordAPIResponseError(ctx, e.cfg, errScan)
					reporter.publishFailure(ctx)
					out <- cliproxyexecutor.StreamChunk{Err: errScan}
				}
				return
			}

			data, errRead := io.ReadAll(resp.Body)
			if errRead != nil {
				recordAPIResponseError(ctx, e.cfg, errRead)
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: errRead}
				return
			}
			appendAPIResponseChunk(ctx, e.cfg, data)
			reporter.publish(ctx, parseGeminiCLIUsage(data))
			var param any
			segments := sdktranslator.TranslateStream(respCtx, to, from, attempt, bytes.Clone(opts.OriginalRequest), reqBody, data, &param)
			for i := range segments {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(segments[i])}
			}

			segments = sdktranslator.TranslateStream(respCtx, to, from, attempt, bytes.Clone(opts.OriginalRequest), reqBody, bytes.Clone([]byte("[DONE]")), &param)
			for i := range segments {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(segments[i])}
			}
		}(httpResp, append([]byte(nil), payload...), attemptModel)

		return stream, nil
	}

	if len(lastBody) > 0 {
		appendAPIResponseChunk(ctx, e.cfg, lastBody)
	}
	if lastStatus == 0 {
		lastStatus = 429
	}
	err = statusErr{code: lastStatus, msg: string(lastBody)}
	return nil, err
}

func (e *GeminiContextAwareExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	tokenSource, baseTokenData, err := prepareGeminiCLITokenSource(ctx, e.cfg, auth)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini-cli")

	models := cliPreviewFallbackOrder(req.Model)
	if len(models) == 0 || models[0] != req.Model {
		models = append([]string{req.Model}, models...)
	}

	httpClient := newHTTPClient(ctx, e.cfg, auth, 0)
	respCtx := context.WithValue(ctx, "alt", opts.Alt)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}

	var lastStatus int
	var lastBody []byte

	budgetOverride, includeOverride, hasOverride := util.GeminiThinkingFromMetadata(req.Metadata)
	for _, attemptModel := range models {
		payload := sdktranslator.TranslateRequest(from, to, attemptModel, bytes.Clone(req.Payload), false)
		if hasOverride && util.ModelSupportsThinking(req.Model) {
			if budgetOverride != nil {
				norm := util.NormalizeThinkingBudget(req.Model, *budgetOverride)
				budgetOverride = &norm
			}
			payload = util.ApplyGeminiCLIThinkingConfig(payload, budgetOverride, includeOverride)
		}
		payload = deleteJSONField(payload, "project")
		payload = deleteJSONField(payload, "model")
		payload = deleteJSONField(payload, "request.safetySettings")
		payload = util.StripThinkingConfigIfUnsupported(req.Model, payload)
		payload = fixGeminiCLIImageAspectRatio(attemptModel, payload)

		tok, errTok := tokenSource.Token()
		if errTok != nil {
			return cliproxyexecutor.Response{}, errTok
		}
		updateGeminiCLITokenMetadata(auth, baseTokenData, tok)

		url := fmt.Sprintf("%s/%s:%s", codeAssistEndpoint, codeAssistVersion, "countTokens")
		if opts.Alt != "" {
			url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
		}

		reqHTTP, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if errReq != nil {
			return cliproxyexecutor.Response{}, errReq
		}
		reqHTTP.Header.Set("Content-Type", "application/json")
		reqHTTP.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		applyGeminiCLIHeaders(reqHTTP)
		reqHTTP.Header.Set("Accept", "application/json")
		recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
			URL:       url,
			Method:    http.MethodPost,
			Headers:   reqHTTP.Header.Clone(),
			Body:      payload,
			Provider:  e.Identifier(),
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})

		resp, errDo := httpClient.Do(reqHTTP)
		if errDo != nil {
			recordAPIResponseError(ctx, e.cfg, errDo)
			return cliproxyexecutor.Response{}, errDo
		}
		data, errRead := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		recordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
		if errRead != nil {
			recordAPIResponseError(ctx, e.cfg, errRead)
			return cliproxyexecutor.Response{}, errRead
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			count := gjson.GetBytes(data, "totalTokens").Int()
			translated := sdktranslator.TranslateTokenCount(respCtx, to, from, count, data)
			return cliproxyexecutor.Response{Payload: []byte(translated)}, nil
		}
		lastStatus = resp.StatusCode
		lastBody = append([]byte(nil), data...)
		if resp.StatusCode == 429 {
			log.Debugf("gemini cli executor: rate limited, retrying with next model")
			continue
		}
		break
	}

	if lastStatus == 0 {
		lastStatus = 429
	}
	return cliproxyexecutor.Response{}, statusErr{code: lastStatus, msg: string(lastBody)}
}

func (e *GeminiContextAwareExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("gemini cli executor: refresh called")
	_ = ctx
	return auth, nil
}


// trimContext reduces the size of the request payload to improve performance.
func (e *GeminiContextAwareExecutor) trimContext(payload []byte) []byte {
	const maxMessages = 10

	// Get the messages from the payload
	messages := gjson.GetBytes(payload, "messages").Array()
	if len(messages) <= maxMessages {
		return payload
	}

	// Keep the last `maxMessages` messages and the system message
	var newMessages []gjson.Result
	for _, message := range messages {
		if message.Get("role").String() == "system" {
			newMessages = append(newMessages, message)
		}
	}
	newMessages = append(newMessages, messages[len(messages)-maxMessages:]...)

	// Create the new payload
	newPayload, _ := sjson.SetBytes(payload, "messages", newMessages)

	return newPayload
}

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	codeAssistEndpoint      = "https://cloudcode-pa.googleapis.com"
	codeAssistVersion       = "v1internal"
	geminiOauthClientID     = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	geminiOauthClientSecret = "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"
)

var geminiOauthScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
}

// GeminiContextAwareExecutor talks to the Cloud Code Assist endpoint using OAuth credentials from auth metadata.
// It reduces the amount of context sent to the Gemini API to improve performance.
