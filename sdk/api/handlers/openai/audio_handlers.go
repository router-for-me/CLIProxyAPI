// Package openai provides HTTP handlers for OpenAI API endpoints.
// This file implements the OpenAI-compatible audio/speech endpoint for TTS.
package openai

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/tidwall/gjson"
)

// OpenAIAudioHandler contains the handlers for OpenAI Audio API endpoints.
type OpenAIAudioHandler struct {
	*handlers.BaseAPIHandler
}

// NewOpenAIAudioHandler creates a new OpenAI Audio API handler instance.
func NewOpenAIAudioHandler(apiHandlers *handlers.BaseAPIHandler) *OpenAIAudioHandler {
	return &OpenAIAudioHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *OpenAIAudioHandler) HandlerType() string {
	return "gemini" // Route to Gemini TTS models
}

// Models returns the TTS models supported by this handler.
func (h *OpenAIAudioHandler) Models() []map[string]any {
	return []map[string]any{
		{"id": "tts-1", "object": "model", "owned_by": "openai"},
		{"id": "tts-1-hd", "object": "model", "owned_by": "openai"},
		{"id": "gemini-2.5-flash-preview-tts", "object": "model", "owned_by": "google"},
		{"id": "gemini-2.5-pro-preview-tts", "object": "model", "owned_by": "google"},
	}
}

// voiceMapping maps OpenAI voice names to Gemini voice names.
var voiceMapping = map[string]string{
	"alloy":   "Puck",
	"echo":    "Charon",
	"fable":   "Kore",
	"nova":    "Aoede",
	"onyx":    "Fenrir",
	"shimmer": "Leda",
}

// modelMapping maps OpenAI TTS models to Gemini TTS models.
var modelMapping = map[string]string{
	"tts-1":    "gemini-2.5-flash-preview-tts",
	"tts-1-hd": "gemini-2.5-pro-preview-tts",
}

// contentTypeMapping maps response formats to MIME types.
var contentTypeMapping = map[string]string{
	"mp3":  "audio/mpeg",
	"opus": "audio/ogg",
	"aac":  "audio/aac",
	"flac": "audio/flac",
	"wav":  "audio/wav",
	"pcm":  "audio/pcm",
}

// SpeechRequest represents the OpenAI audio/speech request format.
type SpeechRequest struct {
	Model          string   `json:"model"`
	Input          string   `json:"input"`
	Voice          string   `json:"voice"`
	ResponseFormat string   `json:"response_format,omitempty"`
	Speed          float64  `json:"speed,omitempty"`
	// Extended fields for Gemini features
	Speakers       []Speaker `json:"speakers,omitempty"` // Multi-speaker support
}

// Speaker represents a speaker configuration for multi-speaker TTS.
type Speaker struct {
	Name  string `json:"name"`
	Voice string `json:"voice"`
}

// Speech handles the POST /v1/audio/speech endpoint.
// It translates OpenAI TTS requests to Gemini TTS format.
func (h *OpenAIAudioHandler) Speech(c *gin.Context) {
	rawJSON, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Parse the request
	var req SpeechRequest
	if err := json.Unmarshal(rawJSON, &req); err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid JSON: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Validate required fields
	if req.Input == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "input is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Map model name
	geminiModel := req.Model
	if mapped, ok := modelMapping[req.Model]; ok {
		geminiModel = mapped
	}

	// Map voice name
	geminiVoice := req.Voice
	if geminiVoice == "" {
		geminiVoice = "Puck" // Default voice
	} else if mapped, ok := voiceMapping[strings.ToLower(req.Voice)]; ok {
		geminiVoice = mapped
	}

	// Set default response format
	responseFormat := req.ResponseFormat
	if responseFormat == "" {
		responseFormat = "mp3"
	}

	// Build Gemini request
	geminiRequest := h.buildGeminiTTSRequest(req.Input, geminiVoice, req.Speakers)

	// Execute via auth manager
	ctx, cancel := h.GetContextWithCancel(h, c, c.Request.Context())
	defer cancel()

	respBytes, errMsg := h.ExecuteWithAuthManager(ctx, h.HandlerType(), geminiModel, geminiRequest, "")
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		return
	}

	// Extract audio from Gemini response
	audioData, err := h.extractAudioFromResponse(respBytes)
	if err != nil {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{
			StatusCode: http.StatusInternalServerError,
			Error:      fmt.Errorf("failed to extract audio: %v", err),
		})
		return
	}

	// Convert audio format if needed
	outputData, err := h.convertAudioFormat(audioData, responseFormat)
	if err != nil {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{
			StatusCode: http.StatusInternalServerError,
			Error:      fmt.Errorf("failed to convert audio format: %v", err),
		})
		return
	}

	// Set content type and return audio
	contentType := contentTypeMapping[responseFormat]
	if contentType == "" {
		contentType = "audio/mpeg"
	}
	c.Data(http.StatusOK, contentType, outputData)
}

// buildGeminiTTSRequest creates a Gemini generateContent request for TTS.
func (h *OpenAIAudioHandler) buildGeminiTTSRequest(text, voice string, speakers []Speaker) []byte {
	// Build speech config
	var speechConfig map[string]any

	if len(speakers) > 0 {
		// Multi-speaker mode
		speakerConfigs := make([]map[string]any, len(speakers))
		for i, s := range speakers {
			voiceName := s.Voice
			if mapped, ok := voiceMapping[strings.ToLower(s.Voice)]; ok {
				voiceName = mapped
			}
			speakerConfigs[i] = map[string]any{
				"speaker": s.Name,
				"voiceConfig": map[string]any{
					"prebuiltVoiceConfig": map[string]any{
						"voiceName": voiceName,
					},
				},
			}
		}
		speechConfig = map[string]any{
			"multiSpeakerVoiceConfig": map[string]any{
				"speakerVoiceConfigs": speakerConfigs,
			},
		}
	} else {
		// Single speaker mode
		speechConfig = map[string]any{
			"voiceConfig": map[string]any{
				"prebuiltVoiceConfig": map[string]any{
					"voiceName": voice,
				},
			},
		}
	}

	request := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{"text": text},
				},
			},
		},
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig":       speechConfig,
		},
	}

	jsonBytes, _ := json.Marshal(request)
	return jsonBytes
}

// extractAudioFromResponse extracts base64 audio data from Gemini response.
func (h *OpenAIAudioHandler) extractAudioFromResponse(respBytes []byte) ([]byte, error) {
	// Parse the Gemini response to extract audio data
	// Response format: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/pcm","data":"BASE64..."}}]}}]}

	dataPath := "candidates.0.content.parts.0.inlineData.data"
	result := gjson.GetBytes(respBytes, dataPath)
	if !result.Exists() {
		return nil, fmt.Errorf("no audio data in response")
	}

	// Decode base64 audio
	audioData, err := base64.StdEncoding.DecodeString(result.String())
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 audio: %v", err)
	}

	return audioData, nil
}

// convertAudioFormat converts PCM audio to the requested format using ffmpeg.
func (h *OpenAIAudioHandler) convertAudioFormat(pcmData []byte, format string) ([]byte, error) {
	if format == "pcm" {
		return pcmData, nil
	}

	// Build ffmpeg command
	// Input: PCM 16-bit signed little-endian, 24kHz, mono
	args := []string{
		"-f", "s16le",
		"-ar", "24000",
		"-ac", "1",
		"-i", "pipe:0",
	}

	// Output format
	switch format {
	case "mp3":
		args = append(args, "-f", "mp3", "-q:a", "2")
	case "wav":
		args = append(args, "-f", "wav")
	case "opus":
		args = append(args, "-c:a", "libopus", "-f", "ogg")
	case "aac":
		args = append(args, "-c:a", "aac", "-f", "adts")
	case "flac":
		args = append(args, "-f", "flac")
	default:
		args = append(args, "-f", "mp3", "-q:a", "2")
	}

	args = append(args, "pipe:1")

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdin = bytes.NewReader(pcmData)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg conversion failed: %v (stderr: %s)", err, stderr.String())
	}

	return stdout.Bytes(), nil
}
