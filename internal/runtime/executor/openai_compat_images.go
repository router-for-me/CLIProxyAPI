package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAICompatAltMiniMaxImageGeneration = "minimax/image_generation"

func isOpenAICompatMiniMaxImageGeneration(opts cliproxyexecutor.Options, profile openAICompatProfile, baseURL string, model string) bool {
	if opts.Alt != openAICompatAltMiniMaxImageGeneration {
		return false
	}
	if !isMiniMaxImageGenerationModel(model) {
		return false
	}
	if config.NormalizeOpenAICompatibilityKind(profile.Kind) == "minimax" {
		return true
	}
	return inferOpenAICompatKindFromBaseURL(baseURL) == "minimax"
}

func isMiniMaxImageGenerationModel(model string) bool {
	switch miniMaxImageGenerationBaseModel(model) {
	case "image-01", "image-01-live":
		return true
	default:
		return false
	}
}

func miniMaxImageGenerationBaseModel(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		model = model[idx+1:]
	}
	if idx := strings.Index(model, "("); idx > 0 {
		model = model[:idx]
	}
	return strings.ToLower(strings.TrimSpace(model))
}

func (e *OpenAICompatExecutor) executeMiniMaxImageGeneration(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, baseURL string, profile openAICompatProfile, reporter *helps.UsageReporter) (cliproxyexecutor.Response, error) {
	payload := req.Payload
	if len(payload) == 0 || !json.Valid(payload) {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadRequest, msg: "minimax image_generation: request body must be valid JSON"}
	}
	upstreamModel := miniMaxImageGenerationBaseModel(req.Model)
	if upstreamModel == "" {
		upstreamModel = strings.TrimSpace(req.Model)
	}
	payload, _ = sjson.SetBytes(payload, "model", upstreamModel)

	url := strings.TrimSuffix(baseURL, "/") + "/image_generation"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return cliproxyexecutor.Response{}, err
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      payload,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return cliproxyexecutor.Response{}, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close minimax image response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return cliproxyexecutor.Response{}, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), body))
		return cliproxyexecutor.Response{}, newOpenAICompatStatusErr(profile, auth, req.Model, httpResp.StatusCode, httpResp.Header, httpResp.Header.Get("Content-Type"), body)
	}

	out, err := buildOpenAIImagesResponseFromMiniMax(body)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	reporter.Publish(ctx, helps.ParseOpenAIUsage(out))
	reporter.EnsurePublished(ctx)
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

func buildOpenAIImagesResponseFromMiniMax(body []byte) ([]byte, error) {
	if len(body) == 0 || !json.Valid(body) {
		return nil, statusErr{code: http.StatusBadGateway, msg: "minimax image_generation: invalid JSON response", errorCode: "minimax_invalid_json"}
	}
	if baseResp := gjson.GetBytes(body, "base_resp.status_code"); baseResp.Exists() && baseResp.Int() != 0 {
		message := firstNonEmptyJSONValue(body, "base_resp.status_msg", "base_resp.message", "base_resp.msg", "message", "msg")
		if message == "" {
			message = "minimax image_generation returned a logical error"
		}
		return nil, statusErr{
			code:               http.StatusBadGateway,
			providerStatusCode: http.StatusOK,
			msg:                message,
			errorCode:          "minimax_" + strings.TrimSpace(baseResp.String()),
		}
	}

	out := []byte(`{"created":0,"data":[]}`)
	out, _ = sjson.SetBytes(out, "created", time.Now().Unix())

	b64Images := collectMiniMaxImageStrings(body,
		"data.image_base64",
		"data.image_base64s",
		"data.images.#.image_base64",
		"data.images.#.b64_json",
		"data.images.#.base64",
		"image_base64",
	)
	urlImages := collectMiniMaxImageStrings(body,
		"data.image_urls",
		"data.image_url",
		"data.images.#.url",
		"data.images.#.image_url",
		"image_urls",
	)

	for _, b64 := range b64Images {
		item := []byte(`{}`)
		item, _ = sjson.SetBytes(item, "b64_json", b64)
		out, _ = sjson.SetRawBytes(out, "data.-1", item)
	}
	for _, imageURL := range urlImages {
		item := []byte(`{}`)
		item, _ = sjson.SetBytes(item, "url", imageURL)
		out, _ = sjson.SetRawBytes(out, "data.-1", item)
	}
	if revisedPrompt := firstNonEmptyJSONValue(body, "data.revised_prompt", "revised_prompt"); revisedPrompt != "" {
		data := gjson.GetBytes(out, "data")
		for idx := range data.Array() {
			path := fmt.Sprintf("data.%d.revised_prompt", idx)
			out, _ = sjson.SetBytes(out, path, revisedPrompt)
		}
	}
	if gjson.GetBytes(out, "data").Array() == nil || len(gjson.GetBytes(out, "data").Array()) == 0 {
		return nil, statusErr{code: http.StatusBadGateway, msg: "minimax image_generation: upstream did not return image output", errorCode: "minimax_empty_output"}
	}
	return out, nil
}

func collectMiniMaxImageStrings(body []byte, paths ...string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	for _, path := range paths {
		result := gjson.GetBytes(body, path)
		if !result.Exists() {
			continue
		}
		if result.IsArray() {
			for _, entry := range result.Array() {
				add(entry.String())
			}
			continue
		}
		add(result.String())
	}
	return out
}
