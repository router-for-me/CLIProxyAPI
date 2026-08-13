// Package openai provides HTTP handlers for OpenAI-compatible API endpoints.
// This file implements the speech (text-to-speech) endpoints: the OpenAI-shaped
// /v1/audio/speech and the xAI-native /v1/tts passthrough.
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	xaiSpeechHandlerType  = "openai-audio"
	xaiSpeechModel        = "grok-tts"
	xaiSpeechDefaultVoice = "eve"
	// xaiSpeechDefaultLanguage is sent when the caller does not specify one:
	// xAI requires the field, and the OpenAI speech schema has no equivalent.
	xaiSpeechDefaultLanguage = "auto"
	// xaiSpeechMaxTextLength mirrors the xAI /v1/tts input limit. It is larger
	// than OpenAI's 4096, and rejecting text the upstream would accept would make
	// the endpoint less useful than the provider it fronts.
	xaiSpeechMaxTextLength = 15000
	xaiSpeechMinSpeed      = 0.7
	xaiSpeechMaxSpeed      = 1.5
	openAISpeechMinSpeed   = 0.25
	openAISpeechMaxSpeed   = 4.0
	audioSpeechPath        = "/v1/audio/speech"
	ttsPath                = "/v1/tts"
)

// xaiSpeechExtraFields lists xAI-only request fields accepted on /v1/audio/speech
// so callers can reach them without switching to the native endpoint. It is an
// allowlist rather than a blind merge of unknown keys.
var xaiSpeechExtraFields = []string{
	"optimize_streaming_latency",
	"text_normalization",
	"with_timestamps",
	"replace",
}

// speechModelParts splits an optional routing prefix from a speech model id,
// reusing the same convention as the image endpoints (e.g. "xai/grok-tts").
func isXAISpeechModel(model string) bool {
	prefix, baseModel := imagesModelParts(model)
	if !strings.EqualFold(strings.TrimSpace(baseModel), xaiSpeechModel) {
		return false
	}
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	return prefix == "" || prefix == "xai" || prefix == "x-ai" || prefix == "grok"
}

func isOpenAICompatSpeechModel(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	info := registry.LookupModelInfo(model)
	return info != nil && info.Type == registry.OpenAISpeechModelType
}

func isSupportedSpeechModel(model string) bool {
	return isXAISpeechModel(model) || isOpenAICompatSpeechModel(model)
}

func rejectUnsupportedSpeechModel(c *gin.Context, model string) bool {
	if isSupportedSpeechModel(model) {
		return false
	}
	writeSpeechError(c, http.StatusBadRequest, fmt.Sprintf("Model %s is not supported on %s. Use %s, or a configured openai-compatibility speech model.", model, audioSpeechPath, xaiSpeechModel))
	return true
}

func writeSpeechError(c *gin.Context, status int, message string) {
	c.JSON(status, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: message,
			Type:    "invalid_request_error",
		},
	})
}

// clampXAISpeechSpeed maps an OpenAI speed (0.25-4.0) into the narrower xAI range
// (0.7-1.5). Clamping rather than rejecting keeps legal OpenAI values working.
func clampXAISpeechSpeed(speed float64) float64 {
	if speed < xaiSpeechMinSpeed {
		return xaiSpeechMinSpeed
	}
	if speed > xaiSpeechMaxSpeed {
		return xaiSpeechMaxSpeed
	}
	return speed
}

// xaiSpeechCodecForResponseFormat maps an OpenAI response_format to an xAI codec.
// opus/aac/flac are legal OpenAI formats with no xAI equivalent; they are rejected
// rather than silently substituted, since returning mp3 bytes labelled flac would
// corrupt the caller's output.
func xaiSpeechCodecForResponseFormat(responseFormat string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(responseFormat)) {
	case "", "mp3":
		return "mp3", true
	case "wav":
		return "wav", true
	case "pcm":
		return "pcm", true
	case "mulaw":
		return "mulaw", true
	case "alaw":
		return "alaw", true
	default:
		return "", false
	}
}

// speechContentType derives the response Content-Type from the requested codec.
// Upstream response headers are not available here: the executor pipeline drops
// them unless passthrough-headers is enabled.
func speechContentType(codec string, withTimestamps bool) string {
	if withTimestamps {
		return "application/json"
	}
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "mp3":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "pcm":
		return "audio/pcm"
	case "opus":
		return "audio/opus"
	case "aac":
		return "audio/aac"
	case "flac":
		return "audio/flac"
	case "mulaw":
		return "audio/basic"
	case "alaw":
		return "audio/x-alaw-basic"
	default:
		return "application/octet-stream"
	}
}

// buildXAISpeechRequest translates an OpenAI /v1/audio/speech body into the xAI
// /v1/tts wire shape. The model field is deliberately dropped: xAI's speech
// endpoint takes no model, and the id is only used for provider routing.
func buildXAISpeechRequest(rawJSON []byte) (payload []byte, contentType string, errMessage string) {
	text := gjson.GetBytes(rawJSON, "input").String()
	if strings.TrimSpace(text) == "" {
		return nil, "", "Invalid request: input is required"
	}
	if len(text) > xaiSpeechMaxTextLength {
		return nil, "", fmt.Sprintf("Invalid request: input exceeds the %d character limit", xaiSpeechMaxTextLength)
	}

	voice := strings.TrimSpace(gjson.GetBytes(rawJSON, "voice").String())
	if voice == "" {
		return nil, "", "Invalid request: voice is required"
	}

	responseFormat := strings.TrimSpace(gjson.GetBytes(rawJSON, "response_format").String())
	codec, ok := xaiSpeechCodecForResponseFormat(responseFormat)
	if !ok {
		return nil, "", fmt.Sprintf("Invalid request: response_format %q is not supported by %s; use mp3, wav, pcm, mulaw, or alaw", responseFormat, xaiSpeechModel)
	}

	if instructions := strings.TrimSpace(gjson.GetBytes(rawJSON, "instructions").String()); instructions != "" {
		log.Debugf("openai speech: dropping instructions field, %s has no equivalent parameter", xaiSpeechModel)
	}

	language := strings.TrimSpace(gjson.GetBytes(rawJSON, "language").String())
	if language == "" {
		// hermes-agent and other OpenAI SDK callers pass lang_code via extra_body.
		language = strings.TrimSpace(gjson.GetBytes(rawJSON, "lang_code").String())
	}
	if language == "" {
		language = xaiSpeechDefaultLanguage
	}

	out := []byte(`{}`)
	out, _ = sjson.SetBytes(out, "text", text)
	out, _ = sjson.SetBytes(out, "voice_id", voice)
	out, _ = sjson.SetBytes(out, "language", language)

	sampleRate := gjson.GetBytes(rawJSON, "sample_rate")
	bitRate := gjson.GetBytes(rawJSON, "bit_rate")
	// Only send output_format when it carries information: xAI defaults to mp3.
	if codec != "mp3" || sampleRate.Type == gjson.Number || bitRate.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "output_format.codec", codec)
		if sampleRate.Type == gjson.Number {
			out, _ = sjson.SetBytes(out, "output_format.sample_rate", sampleRate.Int())
		}
		if codec == "mp3" && bitRate.Type == gjson.Number {
			out, _ = sjson.SetBytes(out, "output_format.bit_rate", bitRate.Int())
		}
	}

	if speed := gjson.GetBytes(rawJSON, "speed"); speed.Type == gjson.Number {
		if speed.Float() < openAISpeechMinSpeed || speed.Float() > openAISpeechMaxSpeed {
			return nil, "", fmt.Sprintf("Invalid request: speed must be between %g and %g", openAISpeechMinSpeed, openAISpeechMaxSpeed)
		}
		out, _ = sjson.SetBytes(out, "speed", clampXAISpeechSpeed(speed.Float()))
	}

	for _, field := range xaiSpeechExtraFields {
		if value := gjson.GetBytes(rawJSON, field); value.Exists() {
			out, _ = sjson.SetRawBytes(out, field, []byte(value.Raw))
		}
	}

	withTimestamps := gjson.GetBytes(rawJSON, "with_timestamps").Bool()
	return out, speechContentType(codec, withTimestamps), ""
}

// buildXAITTSPassthroughRequest validates a native xAI /v1/tts body and strips the
// model field, which is routing metadata on this proxy and not an xAI parameter.
func buildXAITTSPassthroughRequest(rawJSON []byte) (payload []byte, routingModel string, contentType string, errMessage string) {
	text := gjson.GetBytes(rawJSON, "text").String()
	if strings.TrimSpace(text) == "" {
		return nil, "", "", "Invalid request: text is required"
	}
	if len(text) > xaiSpeechMaxTextLength {
		return nil, "", "", fmt.Sprintf("Invalid request: text exceeds the %d character limit", xaiSpeechMaxTextLength)
	}

	routingModel = strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	if routingModel == "" {
		routingModel = xaiSpeechModel
	}

	out := rawJSON
	if gjson.GetBytes(out, "model").Exists() {
		out, _ = sjson.DeleteBytes(out, "model")
	}
	if strings.TrimSpace(gjson.GetBytes(out, "language").String()) == "" {
		out, _ = sjson.SetBytes(out, "language", xaiSpeechDefaultLanguage)
	}
	if strings.TrimSpace(gjson.GetBytes(out, "voice_id").String()) == "" {
		out, _ = sjson.SetBytes(out, "voice_id", xaiSpeechDefaultVoice)
	}

	codec := strings.TrimSpace(gjson.GetBytes(out, "output_format.codec").String())
	if codec == "" {
		codec = "mp3"
	}
	withTimestamps := gjson.GetBytes(out, "with_timestamps").Bool()
	return out, routingModel, speechContentType(codec, withTimestamps), ""
}

// readSpeechRequestBody reads and validates the JSON envelope shared by both
// speech routes.
func readSpeechRequestBody(c *gin.Context) ([]byte, bool) {
	rawJSON, err := handlers.ReadRequestBody(c)
	if err != nil {
		writeSpeechError(c, http.StatusBadRequest, fmt.Sprintf("Invalid request: %v", err))
		return nil, false
	}
	if !json.Valid(rawJSON) {
		writeSpeechError(c, http.StatusBadRequest, "Invalid request: body must be valid JSON")
		return nil, false
	}
	return rawJSON, true
}

// rejectSpeechInHomeMode reports whether the request must be refused because Home
// dispatch is active. The Home control center exposes no speech endpoint, so the
// request would otherwise fail obscurely upstream.
func (h *OpenAIAPIHandler) rejectSpeechInHomeMode(c *gin.Context) bool {
	if h == nil || h.BaseAPIHandler == nil || h.AuthManager == nil || !h.AuthManager.HomeEnabled() {
		return false
	}
	c.JSON(http.StatusServiceUnavailable, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: "Speech endpoints are not available while Home is enabled",
			Type:    "server_error",
		},
	})
	return true
}

// AudioSpeech handles POST /v1/audio/speech using the OpenAI request schema.
func (h *OpenAIAPIHandler) AudioSpeech(c *gin.Context) {
	if h.rejectSpeechInHomeMode(c) {
		return
	}
	rawJSON, ok := readSpeechRequestBody(c)
	if !ok {
		return
	}

	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(rawJSON, "stream_format").String()), "sse") {
		writeSpeechError(c, http.StatusBadRequest, "Invalid request: stream_format \"sse\" is not supported")
		return
	}

	model := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	if model == "" {
		writeSpeechError(c, http.StatusBadRequest, "Invalid request: model is required")
		return
	}
	if rejectUnsupportedSpeechModel(c, model) {
		return
	}

	if isXAISpeechModel(model) {
		xaiReq, contentType, errMessage := buildXAISpeechRequest(rawJSON)
		if errMessage != "" {
			writeSpeechError(c, http.StatusBadRequest, errMessage)
			return
		}
		h.collectSpeech(c, xaiReq, xaiSpeechModel, contentType)
		return
	}

	// openai-compatibility upstreams speak the OpenAI schema natively, so the body
	// is forwarded unchanged and the full OpenAI codec set stays available.
	text := gjson.GetBytes(rawJSON, "input").String()
	if strings.TrimSpace(text) == "" {
		writeSpeechError(c, http.StatusBadRequest, "Invalid request: input is required")
		return
	}
	responseFormat := strings.TrimSpace(gjson.GetBytes(rawJSON, "response_format").String())
	if responseFormat == "" {
		responseFormat = "mp3"
	}
	compatReq, _ := sjson.DeleteBytes(rawJSON, "stream_format")
	h.collectSpeech(c, compatReq, model, speechContentType(responseFormat, false))
}

// XAITTS handles POST /v1/tts, the xAI-native passthrough.
func (h *OpenAIAPIHandler) XAITTS(c *gin.Context) {
	if h.rejectSpeechInHomeMode(c) {
		return
	}
	rawJSON, ok := readSpeechRequestBody(c)
	if !ok {
		return
	}

	xaiReq, routingModel, contentType, errMessage := buildXAITTSPassthroughRequest(rawJSON)
	if errMessage != "" {
		writeSpeechError(c, http.StatusBadRequest, errMessage)
		return
	}
	if !isXAISpeechModel(routingModel) {
		writeSpeechError(c, http.StatusBadRequest, fmt.Sprintf("Model %s is not supported on %s; use %s", routingModel, ttsPath, xaiSpeechModel))
		return
	}
	h.collectSpeech(c, xaiReq, xaiSpeechModel, contentType)
}

// XAITTSVoices handles GET /v1/tts/voices, listing the voices available upstream.
func (h *OpenAIAPIHandler) XAITTSVoices(c *gin.Context) {
	if h.rejectSpeechInHomeMode(c) {
		return
	}
	h.collectSpeech(c, nil, xaiSpeechModel, "application/json")
}

// collectSpeech executes a speech request and writes the upstream bytes back.
//
// Two deliberate differences from the image endpoints, both because this response
// body is binary rather than JSON:
//   - Content-Type is derived from the requested codec, since the executor drops
//     upstream headers unless passthrough-headers is enabled.
//   - StartNonStreamingKeepAlive is not used: it writes newlines into the response
//     body, which is invisible ahead of JSON but corrupts an audio payload.
func (h *OpenAIAPIHandler) collectSpeech(c *gin.Context, req []byte, model string, contentType string) {
	c.Header("Content-Type", contentType)

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())

	resp, upstreamHeaders, errMsg := h.ExecuteSpeechWithAuthManager(cliCtx, xaiSpeechHandlerType, strings.TrimSpace(model), req, "")
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel(nil)
}
