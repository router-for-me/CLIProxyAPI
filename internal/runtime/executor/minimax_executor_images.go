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

	minimaxauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/minimax"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// isMinimaxImageRequest reports whether the request targets the image pipeline.
func isMinimaxImageRequest(opts cliproxyexecutor.Options) bool {
	return strings.EqualFold(strings.TrimSpace(opts.SourceFormat.String()), minimaxImageHandlerType)
}

// minimaxImageURL resolves the MiniMax image generation endpoint.
func minimaxImageURL(auth *cliproxyauth.Auth) string {
	base := minimaxauth.ResolveBaseURL(auth)
	if strings.HasSuffix(base, "/v1") {
		return base + "/image_generation"
	}
	return base + "/v1/image_generation"
}

// applyMinimaxImageHeaders authenticates against the MiniMax image endpoint.
// Unlike the Anthropic-compatible messages endpoint, this path expects a bearer
// token rather than x-api-key.
func applyMinimaxImageHeaders(req *http.Request, auth *cliproxyauth.Auth) {
	if req == nil {
		return
	}
	if token := minimaxToken(auth); token != "" {
		req.Header.Del("x-api-key")
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
}

// buildMinimaxImagePayload translates the OpenAI image request shape into the
// MiniMax /v1/image_generation shape.
func buildMinimaxImagePayload(body []byte) ([]byte, error) {
	if !json.Valid(body) {
		return nil, fmt.Errorf("minimax executor: image request body is not valid JSON")
	}
	payload := []byte(`{"model":"image-01","prompt":""}`)
	if model := strings.TrimSpace(gjson.GetBytes(body, "model").String()); model != "" {
		payload, _ = sjson.SetBytes(payload, "model", model)
	}
	payload, _ = sjson.SetBytes(payload, "prompt", gjson.GetBytes(body, "prompt").String())
	// Always send a value MiniMax accepts, even when the caller omitted one.
	payload, _ = sjson.SetBytes(payload, "response_format",
		normalizeMinimaxImageResponseFormat(gjson.GetBytes(body, "response_format").String()))

	if n := gjson.GetBytes(body, "n"); n.Exists() && n.Type == gjson.Number && n.Int() > 0 {
		payload, _ = sjson.SetBytes(payload, "n", n.Int())
	}
	if aspect := strings.TrimSpace(gjson.GetBytes(body, "aspect_ratio").String()); aspect != "" {
		payload, _ = sjson.SetBytes(payload, "aspect_ratio", aspect)
	}
	if seed := gjson.GetBytes(body, "seed"); seed.Exists() && seed.Type == gjson.Number {
		payload, _ = sjson.SetBytes(payload, "seed", seed.Int())
	}
	if width := gjson.GetBytes(body, "width"); width.Exists() && width.Type == gjson.Number {
		payload, _ = sjson.SetBytes(payload, "width", width.Int())
	}
	if height := gjson.GetBytes(body, "height"); height.Exists() && height.Type == gjson.Number {
		payload, _ = sjson.SetBytes(payload, "height", height.Int())
	}
	return payload, nil
}

// normalizeMinimaxImageResponseFormat maps the OpenAI response_format values
// onto the ones MiniMax accepts. MiniMax rejects anything other than "url" or
// "base64", so OpenAI's "b64_json" must be translated rather than forwarded.
func normalizeMinimaxImageResponseFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "url":
		return "url"
	case "base64":
		return "base64"
	default:
		return "base64"
	}
}

// normalizeMinimaxImageResponse converts the MiniMax image response into the
// flat OpenAI `data[]` shape the shared image handler parses.
func normalizeMinimaxImageResponse(payload []byte) ([]byte, error) {
	if !json.Valid(payload) {
		return nil, fmt.Errorf("minimax executor: image response is not valid JSON")
	}
	// A provider-level error must not be mistaken for an empty success payload.
	if baseResp := gjson.GetBytes(payload, "base_resp"); baseResp.Exists() {
		if code := baseResp.Get("status_code"); code.Exists() && code.Int() != 0 {
			return nil, fmt.Errorf("minimax image generation failed (status %d): %s",
				code.Int(), baseResp.Get("status_msg").String())
		}
	}

	out := []byte(`{"created":0,"data":[]}`)
	out, _ = sjson.SetBytes(out, "created", time.Now().Unix())

	// MiniMax returns base64 or URLs depending on the requested format. Walk
	// both arrays by index so a response carrying either or both keeps the
	// original image ordering.
	urls := gjson.GetBytes(payload, "data.image_urls")
	b64s := gjson.GetBytes(payload, "data.image_base64")
	urlItems := []string{}
	if urls.IsArray() {
		urls.ForEach(func(_, item gjson.Result) bool {
			urlItems = append(urlItems, strings.TrimSpace(item.String()))
			return true
		})
	}
	b64Items := []string{}
	if b64s.IsArray() {
		b64s.ForEach(func(_, item gjson.Result) bool {
			b64Items = append(b64Items, strings.TrimSpace(item.String()))
			return true
		})
	}

	total := len(urlItems)
	if len(b64Items) > total {
		total = len(b64Items)
	}
	for i := 0; i < total; i++ {
		entry := []byte(`{}`)
		entry, _ = sjson.SetBytes(entry, "mime_type", "image/jpeg")
		written := false
		if i < len(b64Items) && b64Items[i] != "" {
			entry, _ = sjson.SetBytes(entry, "b64_json", b64Items[i])
			written = true
		}
		if i < len(urlItems) && urlItems[i] != "" {
			entry, _ = sjson.SetBytes(entry, "url", urlItems[i])
			written = true
		}
		if written {
			out, _ = sjson.SetRawBytes(out, "data.-1", entry)
		}
	}

	if len(gjson.GetBytes(out, "data").Array()) == 0 {
		return nil, fmt.Errorf("minimax image generation returned no images")
	}
	return out, nil
}

// executeMinimaxImage performs a non-streaming image generation request.
func (e *MinimaxExecutor) executeMinimaxImage(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := gjson.GetBytes(req.Payload, "model").String()
	if strings.TrimSpace(baseModel) == "" {
		baseModel = gjson.GetBytes(opts.OriginalRequest, "model").String()
	}
	if strings.TrimSpace(baseModel) == "" {
		return cliproxyexecutor.Response{}, fmt.Errorf("minimax executor: image request is missing a model")
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	var err error
	defer func() { reporter.TrackFailure(ctx, &err) }()

	// Payload configuration is the final semantic barrier: all translation and
	// normalization above must be complete before this runs.
	ctx = helps.WithPayloadFinalizer(ctx, helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel,
		minimaxImageHandlerType, "", opts.OriginalRequest, req, opts))

	body, err := buildMinimaxImagePayload(req.Payload)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	body = helps.FinalizePayload(ctx, body)

	url := minimaxImageURL(auth)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	applyMinimaxImageHeaders(httpReq, auth)

	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:      url,
		Method:   http.MethodPost,
		Headers:  httpReq.Header.Clone(),
		Body:     body,
		Provider: e.Identifier(),
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return cliproxyexecutor.Response{}, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("minimax images: close body error: %v", errClose)
		}
	}()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("minimax executor: failed to read image response: %w", err)
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return cliproxyexecutor.Response{}, fmt.Errorf("minimax image generation failed with status %d: %s",
			httpResp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	normalized, err := normalizeMinimaxImageResponse(respBody)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{
		Payload: normalized,
		Headers: httpResp.Header.Clone(),
	}, nil
}

// executeMinimaxImageStream performs an image generation request and emits the
// normalized payload as a single stream chunk, matching the shared image
// handler's streaming expectations.
func (e *MinimaxExecutor) executeMinimaxImageStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	nonStreamOpts := opts
	nonStreamOpts.Stream = false

	result, err := e.executeMinimaxImage(ctx, auth, req, nonStreamOpts)
	if err != nil {
		return nil, err
	}

	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: result.Payload}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Headers: result.Headers, Chunks: chunks}, nil
}
