package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/tidwall/gjson"
)

const (
	xaiSpeechHandlerType       = "openai-speech"
	defaultXAISpeechModel      = "grok-tts"
	xaiSpeechVoiceModel        = "grok-voice-tts-1.0"
	defaultXAISpeechVoice      = "eve"
	maxXAISpeechRunes          = 60000
	maxXAISpeechBody           = 1 << 20
	defaultXAISpeechSampleRate = 24000
)

// openAISpeechVoices maps OpenAI TTS voice names onto Grok voices.
// Unknown names pass through so Grok voice ids keep working.
var openAISpeechVoices = map[string]string{
	"alloy":   "ara",
	"ash":     "orion",
	"ballad":  "luna",
	"coral":   "celeste",
	"echo":    "rex",
	"fable":   "sal",
	"onyx":    "leo",
	"nova":    "eve",
	"sage":    "iris",
	"shimmer": "aurora",
	"verse":   "lumen",
}

func (h *OpenAIAPIHandler) AudioSpeech(c *gin.Context) {
	h.handleXAISpeech(c)
}

func (h *OpenAIAPIHandler) XAITTS(c *gin.Context) {
	h.handleXAISpeech(c)
}

func (h *OpenAIAPIHandler) handleXAISpeech(c *gin.Context) {
	raw, err := handlers.ReadRequestBody(c)
	if err != nil {
		writeSpeechError(c, http.StatusBadRequest, fmt.Sprintf("Invalid request: %v", err))
		return
	}
	if len(raw) > maxXAISpeechBody {
		writeSpeechError(c, http.StatusBadRequest, "request body is larger than 1MB")
		return
	}

	requested := strings.TrimSpace(gjson.GetBytes(raw, "model").String())
	model, ok := speechRoutingModel(requested)
	if !ok {
		writeSpeechError(c, http.StatusBadRequest, fmt.Sprintf("Model %s is not supported on /v1/audio/speech. Use %s.", requested, defaultXAISpeechModel))
		return
	}
	payload, format, err := buildXAISpeechPayload(raw)
	if err != nil {
		writeSpeechError(c, http.StatusBadRequest, err.Error())
		return
	}

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	// Keep-alive newlines would be prefixed onto the audio body.
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiSpeechHandlerType, model, payload, "")
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	c.Header("Content-Type", speechResponseContentType(format, upstreamHeaders.Get("Content-Type")))
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	c.Writer.Header().Del("Content-Length")
	_, _ = c.Writer.Write(resp)
	cliCancel(nil)
}

func writeSpeechError(c *gin.Context, status int, message string) {
	c.JSON(status, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: message,
			Type:    "invalid_request_error",
		},
	})
}

func speechRoutingModel(model string) (string, bool) {
	switch speechModelBase(model) {
	case "", "tts-1", "tts-1-hd", "gpt-4o-mini-tts", defaultXAISpeechModel:
		return defaultXAISpeechModel, true
	case xaiSpeechVoiceModel:
		return xaiSpeechVoiceModel, true
	default:
		return "", false
	}
}

func speechModelBase(model string) string {
	model = strings.TrimSpace(model)
	lower := strings.ToLower(model)
	for _, prefix := range []string{"xai/", "x-ai/", "grok/"} {
		if strings.HasPrefix(lower, prefix) {
			model = strings.TrimSpace(model[len(prefix):])
			break
		}
	}
	if open := strings.LastIndex(model, "("); open > 0 && strings.HasSuffix(model, ")") {
		model = strings.TrimSpace(model[:open])
	}
	return strings.ToLower(strings.TrimSpace(model))
}

func buildXAISpeechPayload(raw []byte) ([]byte, string, error) {
	if !gjson.ValidBytes(raw) {
		return nil, "", errors.New("body must be valid JSON")
	}
	text := strings.TrimSpace(gjson.GetBytes(raw, "input").String())
	if text == "" {
		text = strings.TrimSpace(gjson.GetBytes(raw, "text").String())
	}
	if text == "" {
		return nil, "", errors.New("input is required")
	}
	if utf8.RuneCountInString(text) > maxXAISpeechRunes {
		return nil, "", errors.New("input is longer than 60000 characters")
	}

	voice := strings.TrimSpace(gjson.GetBytes(raw, "voice").String())
	if voice == "" {
		voice = gjson.GetBytes(raw, "voice_id").String()
	}
	language := strings.TrimSpace(gjson.GetBytes(raw, "language").String())
	if language == "" {
		language = "auto"
	}
	body := map[string]any{
		"text":     text,
		"voice_id": mapXAISpeechVoice(voice),
		"language": language,
	}
	if speed := gjson.GetBytes(raw, "speed"); speed.Exists() && speed.Type == gjson.Number && speed.Float() > 0 {
		body["speed"] = speed.Float()
	}
	format, output, err := speechOutputFormat(raw)
	if err != nil {
		return nil, "", err
	}
	if output != nil {
		body["output_format"] = output
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, "", err
	}
	return encoded, format, nil
}

func mapXAISpeechVoice(voice string) string {
	voice = strings.ToLower(strings.TrimSpace(voice))
	if voice == "" {
		return defaultXAISpeechVoice
	}
	if mapped, ok := openAISpeechVoices[voice]; ok {
		return mapped
	}
	return voice
}

func speechOutputFormat(raw []byte) (string, map[string]any, error) {
	responseFormat := strings.ToLower(strings.TrimSpace(gjson.GetBytes(raw, "response_format").String()))
	if responseFormat != "" {
		format, output, err := speechCodecFormat(responseFormat, defaultXAISpeechSampleRate)
		return format, output, err
	}
	native := gjson.GetBytes(raw, "output_format")
	if !native.Exists() {
		return "mp3", nil, nil
	}
	if !native.IsObject() {
		return "", nil, errors.New("output_format must be an object")
	}
	codec := strings.ToLower(strings.TrimSpace(native.Get("codec").String()))
	sampleRate := defaultXAISpeechSampleRate
	if rate := native.Get("sample_rate"); rate.Exists() && rate.Type == gjson.Number && rate.Float() > 0 {
		sampleRate = int(rate.Float())
	}
	return speechCodecFormat(codec, sampleRate)
}

func speechCodecFormat(codec string, sampleRate int) (string, map[string]any, error) {
	switch codec {
	case "", "mp3":
		return "mp3", nil, nil
	case "wav", "pcm":
		if sampleRate <= 0 {
			sampleRate = defaultXAISpeechSampleRate
		}
		return codec, map[string]any{"codec": codec, "sample_rate": sampleRate}, nil
	default:
		return "", nil, errors.New("response_format must be mp3, wav, or pcm")
	}
}

func speechContentType(format string) string {
	switch format {
	case "wav":
		return "audio/wav"
	case "pcm":
		return "audio/pcm"
	default:
		return "audio/mpeg"
	}
}

func speechContentTypeNeedsDefault(contentType string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.Index(mediaType, ";"); i >= 0 {
		mediaType = strings.TrimSpace(mediaType[:i])
	}
	switch mediaType {
	case "", "application/json", "application/octet-stream", "text/plain":
		return true
	default:
		return false
	}
}

func speechResponseContentType(format, upstream string) string {
	if !speechContentTypeNeedsDefault(upstream) {
		return upstream
	}
	return speechContentType(format)
}
