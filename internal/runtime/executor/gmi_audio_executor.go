package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	gmiAudioQueueBaseURL = "https://console.gmicloud.ai/api/v1/ie/requestqueue/apikey/requests"
	gmiAudioPollInterval = 3 * time.Second
	gmiAudioMaxWait      = 5 * time.Minute
	gmiAudioDownloadDir  = "gmi-audio-downloads"
)

// gmiAudioModelMap maps client-facing aliases to GMI Cloud queue model IDs.
var gmiAudioModelMap = map[string]string{
	"minimax-speech-2.8": "minimax-tts-speech-2.8-hd",
	"minimax-music-3.0":  "minimax-music-3.0",
}

// GMIAudioExecutor extends OpenAICompatExecutor to handle GMI Cloud's
// asynchronous audio generation models (Speech 2.8, Music 3.0).
// These models use a request queue API instead of standard chat completions.
type GMIAudioExecutor struct {
	OpenAICompatExecutor
	cfg *config.Config
}

// NewGMIAudioExecutor creates an executor for GMI Cloud audio models.
func NewGMIAudioExecutor(provider string, cfg *config.Config) *GMIAudioExecutor {
	return &GMIAudioExecutor{
		OpenAICompatExecutor: *NewOpenAICompatExecutor(provider, cfg),
		cfg:                  cfg,
	}
}

// Identifier returns the executor identifier.
func (e *GMIAudioExecutor) Identifier() string { return e.OpenAICompatExecutor.Identifier() }

// isAudioModel reports whether the model is a GMI async audio model.
func isAudioModel(model string) bool {
	lower := strings.ToLower(model)
	for alias := range gmiAudioModelMap {
		if strings.HasPrefix(lower, alias) {
			return true
		}
	}
	return false
}

// gmiQueueModelID resolves the upstream queue model ID from a client-facing alias.
func gmiQueueModelID(alias string) string {
	lower := strings.ToLower(alias)
	for prefix, queueModel := range gmiAudioModelMap {
		if strings.HasPrefix(lower, prefix) {
			return queueModel
		}
	}
	return ""
}

// Execute handles non-streaming requests. For audio models, it submits the task
// to GMI's request queue, polls until completion, and returns the audio URL
// wrapped in a standard OpenAI chat completions response.
func (e *GMIAudioExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	if !isAudioModel(baseModel) {
		return e.OpenAICompatExecutor.Execute(ctx, auth, req, opts)
	}

	return e.executeAudio(ctx, auth, req, opts, baseModel, false)
}

// ExecuteStream handles streaming requests. Audio models do not support true
// streaming; we execute synchronously and emit the result as a single chunk.
func (e *GMIAudioExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	if !isAudioModel(baseModel) {
		return e.OpenAICompatExecutor.ExecuteStream(ctx, auth, req, opts)
	}

	resp, err := e.executeAudio(ctx, auth, req, opts, baseModel, true)
	if err != nil {
		return nil, err
	}

	chunkCh := make(chan cliproxyexecutor.StreamChunk, 1)
	chunkCh <- cliproxyexecutor.StreamChunk{Payload: resp.Payload}
	close(chunkCh)

	return &cliproxyexecutor.StreamResult{
		Headers: resp.Headers,
		Chunks:  chunkCh,
	}, nil
}

// extractUserContent pulls the text prompt from an OpenAI-format messages payload.
func extractUserContent(payload []byte) string {
	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		last := messages.Array()
		for i := len(last) - 1; i >= 0; i-- {
			role := last[i].Get("role").String()
			if role == "user" || role == "" {
				content := last[i].Get("content")
				if content.Type == gjson.String {
					return content.String()
				}
				// Handle content array format
				parts := content.Array()
				for _, part := range parts {
					if part.Get("type").String() == "text" {
						return part.Get("text").String()
					}
				}
			}
		}
	}
	// Fallback: check top-level "input" or "prompt"
	if v := gjson.GetBytes(payload, "input"); v.Exists() {
		return v.String()
	}
	if v := gjson.GetBytes(payload, "prompt"); v.Exists() {
		return v.String()
	}
	return ""
}

// executeAudio submits the audio generation request to GMI's async queue and polls for completion.
func (e *GMIAudioExecutor) executeAudio(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	model string,
	stream bool,
) (resp cliproxyexecutor.Response, err error) {
	queueModel := gmiQueueModelID(model)
	if queueModel == "" {
		return resp, fmt.Errorf("gmi audio executor: no queue model mapping for %q", model)
	}

	_, apiKey := e.resolveCredentials(auth)
	if apiKey == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "missing GMI API key"}
	}

	userContent := extractUserContent(opts.OriginalRequest)
	if userContent == "" {
		userContent = extractUserContent(req.Payload)
	}
	if userContent == "" {
		return resp, statusErr{code: http.StatusBadRequest, msg: "no text input found for audio generation"}
	}

	payload := buildGMIAudioPayload(queueModel, userContent)
	requestID, errSubmit := e.submitAudioRequest(ctx, apiKey, payload)
	if errSubmit != nil {
		return resp, errSubmit
	}

	audioURL, errPoll := e.pollAudioResult(ctx, apiKey, requestID)
	if errPoll != nil {
		return resp, errPoll
	}

	localPath := e.downloadAudioFile(audioURL, requestID, queueModel)

	out := buildOpenAIAudioResponse(model, audioURL, localPath, stream)
	resp = cliproxyexecutor.Response{
		Payload: out,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
	}
	return resp, nil
}

// buildGMIAudioPayload constructs the GMI queue request body for TTS or Music.
func buildGMIAudioPayload(queueModel string, text string) map[string]interface{} {
	payload := map[string]interface{}{
		"model": queueModel,
	}

	if strings.Contains(queueModel, "speech") {
		payload["payload"] = map[string]string{
			"text":     text,
			"voice_id": "English_expressive_narrator",
			"speed":    "1",
			"volume":   "1.0",
		}
	} else if strings.Contains(queueModel, "music") {
		payload["payload"] = map[string]string{
			"lyrics": text,
		}
	}

	return payload
}

// submitAudioRequest POSTs the generation job to GMI's request queue.
func (e *GMIAudioExecutor) submitAudioRequest(ctx context.Context, apiKey string, body map[string]interface{}) (string, error) {
	data, _ := json.Marshal(body)
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, gmiAudioQueueBaseURL, bytes.NewReader(data))
	if errReq != nil {
		return "", fmt.Errorf("gmi audio executor: build request failed: %w", errReq)
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 120 * time.Second}
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		return "", fmt.Errorf("gmi audio executor: submit failed: %w", errDo)
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gmi audio executor: close submit response body: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		return "", fmt.Errorf("gmi audio executor: read submit response: %w", errRead)
	}

	if httpResp.StatusCode >= 400 {
		return "", statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
	}

	result := gjson.ParseBytes(bodyBytes)
	requestID := result.Get("request_id").String()
	if requestID == "" {
		return "", fmt.Errorf("gmi audio executor: no request_id in response: %s", string(bodyBytes))
	}
	return requestID, nil
}

// pollAudioResult polls the GMI request queue until the audio generation completes or times out.
func (e *GMIAudioExecutor) pollAudioResult(ctx context.Context, apiKey string, requestID string) (string, error) {
	url := gmiAudioQueueBaseURL + "/" + requestID

	deadline := time.Now().Add(gmiAudioMaxWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("gmi audio executor: context cancelled while polling: %w", ctx.Err())
		case <-time.After(gmiAudioPollInterval):
		}

		httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if errReq != nil {
			return "", fmt.Errorf("gmi audio executor: build poll request: %w", errReq)
		}
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)

		httpClient := &http.Client{Timeout: 30 * time.Second}
		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			log.Debugf("gmi audio executor: poll attempt failed: %v", errDo)
			continue
		}
		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gmi audio executor: close poll response body: %v", errClose)
		}
		if errRead != nil {
			continue
		}

		if httpResp.StatusCode >= 400 {
			return "", statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
		}

		result := gjson.ParseBytes(bodyBytes)
		status := result.Get("status").String()

		switch status {
		case "success":
			audioURL := result.Get("outcome.audio_url").String()
			if audioURL == "" {
				return "", fmt.Errorf("gmi audio executor: success but no audio_url in outcome")
			}
			return audioURL, nil
		case "failed", "error":
			errMsg := result.Get("outcome.error").String()
			if errMsg == "" {
				errMsg = string(bodyBytes)
			}
			return "", statusErr{code: http.StatusInternalServerError, msg: errMsg}
		default:
			// Still processing, continue polling
		}
	}

	return "", fmt.Errorf("gmi audio executor: timed out after %v waiting for request %s", gmiAudioMaxWait, requestID)
}

// downloadAudioFile downloads the generated audio to a local directory and returns the file path.
func (e *GMIAudioExecutor) downloadAudioFile(audioURL string, requestID string, model string) string {
	dir := gmiAudioDownloadDir
	if errMk := os.MkdirAll(dir, 0755); errMk != nil {
		log.Errorf("gmi audio executor: create download dir: %v", errMk)
		return ""
	}

	ext := ".mp3"
	httpClient := &http.Client{Timeout: 120 * time.Second}
	httpReq, errReq := http.NewRequest(http.MethodGet, audioURL, nil)
	if errReq != nil {
		log.Errorf("gmi audio executor: build download request: %v", errReq)
		return ""
	}

	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		log.Errorf("gmi audio executor: download audio: %v", errDo)
		return ""
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gmi audio executor: close download body: %v", errClose)
		}
	}()

	if httpResp.StatusCode >= 400 {
		log.Errorf("gmi audio executor: download got status %d", httpResp.StatusCode)
		return ""
	}

	fileName := fmt.Sprintf("%s_%s%s", model, requestID[:8], ext)
	filePath := filepath.Join(dir, fileName)
	f, errCreate := os.Create(filePath)
	if errCreate != nil {
		log.Errorf("gmi audio executor: create local file: %v", errCreate)
		return ""
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Errorf("gmi audio executor: close local file: %v", errClose)
		}
	}()

	if _, errCopy := io.Copy(f, httpResp.Body); errCopy != nil {
		log.Errorf("gmi audio executor: write audio data: %v", errCopy)
		return ""
	}

	log.Infof("gmi audio executor: saved audio to %s", filePath)
	return filePath
}

// buildOpenAIAudioResponse wraps an audio URL and optional local path into an OpenAI chat completions response.
func buildOpenAIAudioResponse(model string, audioURL string, localPath string, stream bool) []byte {
	contentText := fmt.Sprintf("[audio generated] %s\n[local file] %s", audioURL, localPath)
	response := map[string]interface{}{
		"id":      fmt.Sprintf("chatcmpl-gmi-%d", time.Now().UnixMilli()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       map[string]interface{}{"role": "assistant", "content": contentText},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		},
	}
	data, _ := json.Marshal(response)
	return data
}
