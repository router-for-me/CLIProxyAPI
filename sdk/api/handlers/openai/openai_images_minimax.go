package openai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const miniMaxImageGenerationAlt = "minimax/image_generation"

func isMiniMaxImageModelName(model string) bool {
	switch miniMaxImageBaseModel(model) {
	case "image-01", "image-01-live":
		return true
	default:
		return false
	}
}

func miniMaxImageBaseModel(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		model = model[idx+1:]
	}
	if idx := strings.Index(model, "("); idx > 0 {
		model = model[:idx]
	}
	return strings.ToLower(strings.TrimSpace(model))
}

func buildMiniMaxImageGenerationRequest(rawJSON []byte, model string, prompt string, images []string) ([]byte, string, error) {
	baseModel := miniMaxImageBaseModel(model)
	if baseModel == "" {
		baseModel = strings.TrimSpace(model)
	}
	if baseModel == "" {
		return nil, "", fmt.Errorf("model is required")
	}

	out := []byte(`{"model":"","prompt":""}`)
	out, _ = sjson.SetBytes(out, "model", baseModel)
	out, _ = sjson.SetBytes(out, "prompt", prompt)

	responseFormat := "b64_json"
	miniMaxResponseFormat := "base64"
	if len(rawJSON) > 0 && json.Valid(rawJSON) {
		responseFormat = strings.ToLower(strings.TrimSpace(gjson.GetBytes(rawJSON, "response_format").String()))
	}
	switch responseFormat {
	case "url":
		miniMaxResponseFormat = "url"
	case "base64", "b64_json", "":
		responseFormat = "b64_json"
		miniMaxResponseFormat = "base64"
	default:
		responseFormat = "b64_json"
		miniMaxResponseFormat = "base64"
	}
	out, _ = sjson.SetBytes(out, "response_format", miniMaxResponseFormat)

	if len(rawJSON) > 0 && json.Valid(rawJSON) {
		out = copyMiniMaxImageScalarFields(out, rawJSON)
		out = copyMiniMaxImageSizeFields(out, rawJSON, baseModel)
		if style := gjson.GetBytes(rawJSON, "style"); style.Exists() && style.IsObject() {
			out, _ = sjson.SetRawBytes(out, "style", []byte(style.Raw))
		}
		if subjectReference := gjson.GetBytes(rawJSON, "subject_reference"); subjectReference.Exists() && subjectReference.IsArray() {
			out, _ = sjson.SetRawBytes(out, "subject_reference", []byte(subjectReference.Raw))
			return out, responseFormat, nil
		}
	}

	if len(images) > 0 {
		subjects := []byte(`[]`)
		for _, image := range images {
			image = strings.TrimSpace(image)
			if image == "" {
				continue
			}
			subject := []byte(`{"type":"character","image_file":""}`)
			subject, _ = sjson.SetBytes(subject, "image_file", image)
			subjects, _ = sjson.SetRawBytes(subjects, "-1", subject)
		}
		if len(gjson.ParseBytes(subjects).Array()) > 0 {
			out, _ = sjson.SetRawBytes(out, "subject_reference", subjects)
		}
	}

	return out, responseFormat, nil
}

func copyMiniMaxImageScalarFields(out []byte, rawJSON []byte) []byte {
	for _, field := range []string{"n", "seed"} {
		value := gjson.GetBytes(rawJSON, field)
		if value.Exists() && value.Type == gjson.Number {
			out, _ = sjson.SetRawBytes(out, field, []byte(value.Raw))
		}
	}
	for _, field := range []string{"prompt_optimizer", "aigc_watermark"} {
		value := gjson.GetBytes(rawJSON, field)
		if value.Exists() && (value.Type == gjson.True || value.Type == gjson.False) {
			out, _ = sjson.SetRawBytes(out, field, []byte(value.Raw))
		}
	}
	if aspectRatio := strings.TrimSpace(gjson.GetBytes(rawJSON, "aspect_ratio").String()); aspectRatio != "" {
		out, _ = sjson.SetBytes(out, "aspect_ratio", aspectRatio)
	}
	return out
}

func copyMiniMaxImageSizeFields(out []byte, rawJSON []byte, baseModel string) []byte {
	if baseModel != "image-01" || strings.TrimSpace(gjson.GetBytes(rawJSON, "aspect_ratio").String()) != "" {
		return out
	}

	width := gjson.GetBytes(rawJSON, "width")
	height := gjson.GetBytes(rawJSON, "height")
	if width.Exists() && width.Type == gjson.Number && height.Exists() && height.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "width", width.Int())
		out, _ = sjson.SetBytes(out, "height", height.Int())
		return out
	}

	size := strings.TrimSpace(gjson.GetBytes(rawJSON, "size").String())
	if size == "" || strings.EqualFold(size, "auto") {
		return out
	}
	parsedWidth, parsedHeight, ok := parseOpenAIImageSize(size)
	if !ok {
		return out
	}
	if aspectRatio := miniMaxAspectRatioForSize(parsedWidth, parsedHeight); aspectRatio != "" {
		out, _ = sjson.SetBytes(out, "aspect_ratio", aspectRatio)
		return out
	}
	if miniMaxImageDimensionAllowed(parsedWidth) && miniMaxImageDimensionAllowed(parsedHeight) {
		out, _ = sjson.SetBytes(out, "width", parsedWidth)
		out, _ = sjson.SetBytes(out, "height", parsedHeight)
	}
	return out
}

func parseOpenAIImageSize(size string) (int64, int64, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(size)), "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	width, errWidth := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	height, errHeight := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if errWidth != nil || errHeight != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

func miniMaxImageDimensionAllowed(v int64) bool {
	return v >= 512 && v <= 2048 && v%8 == 0
}

func miniMaxAspectRatioForSize(width int64, height int64) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	gcd := gcdInt64(width, height)
	if gcd <= 0 {
		return ""
	}
	ratio := fmt.Sprintf("%d:%d", width/gcd, height/gcd)
	switch ratio {
	case "1:1", "16:9", "4:3", "3:2", "2:3", "3:4", "9:16", "21:9":
		return ratio
	default:
		return ""
	}
}

func gcdInt64(a int64, b int64) int64 {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func buildMiniMaxImageMultipartJSON(c *gin.Context, model string, prompt string, responseFormat string) []byte {
	raw := []byte(`{"model":"","prompt":""}`)
	raw, _ = sjson.SetBytes(raw, "model", model)
	raw, _ = sjson.SetBytes(raw, "prompt", prompt)
	if strings.TrimSpace(responseFormat) != "" {
		raw, _ = sjson.SetBytes(raw, "response_format", strings.TrimSpace(responseFormat))
	}
	for _, field := range []string{"size", "aspect_ratio"} {
		if v := strings.TrimSpace(c.PostForm(field)); v != "" {
			raw, _ = sjson.SetBytes(raw, field, v)
		}
	}
	for _, field := range []string{"n", "seed", "width", "height"} {
		if v := strings.TrimSpace(c.PostForm(field)); v != "" {
			raw, _ = sjson.SetBytes(raw, field, parseIntField(v, 0))
		}
	}
	for _, field := range []string{"prompt_optimizer", "aigc_watermark"} {
		if v := strings.TrimSpace(c.PostForm(field)); v != "" {
			raw, _ = sjson.SetBytes(raw, field, parseBoolField(v, false))
		}
	}
	return raw
}

func (h *OpenAIAPIHandler) collectMiniMaxImages(c *gin.Context, miniMaxReq []byte, imageModel string) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, c.Request.Context())
	cliCtx = handlers.WithDisallowFreeAuth(cliCtx)
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)

	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), imageModel, miniMaxReq, miniMaxImageGenerationAlt)
	stopKeepAlive()
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

func (h *OpenAIAPIHandler) streamMiniMaxImages(c *gin.Context, miniMaxReq []byte, imageModel string, streamPrefix string) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, c.Request.Context())
	cliCtx = handlers.WithDisallowFreeAuth(cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), imageModel, miniMaxReq, miniMaxImageGenerationAlt)
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	events, err := buildMiniMaxImageStreamEvents(resp, streamPrefix)
	if err != nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
		h.WriteErrorResponse(c, errMsg)
		cliCancel(err)
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	for _, event := range events {
		_, _ = fmt.Fprintf(c.Writer, "event: %s\n", streamPrefix+".completed")
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(event))
		flusher.Flush()
	}
	cliCancel(nil)
}

func buildMiniMaxImageStreamEvents(openAIImagesResponse []byte, streamPrefix string) ([][]byte, error) {
	if !json.Valid(openAIImagesResponse) {
		return nil, fmt.Errorf("invalid image response JSON")
	}
	items := gjson.GetBytes(openAIImagesResponse, "data")
	if !items.IsArray() {
		return nil, fmt.Errorf("image response missing data array")
	}
	eventName := strings.TrimSpace(streamPrefix) + ".completed"
	var events [][]byte
	for _, item := range items.Array() {
		event := []byte(`{"type":""}`)
		event, _ = sjson.SetBytes(event, "type", eventName)
		hasImageData := false
		if b64 := strings.TrimSpace(item.Get("b64_json").String()); b64 != "" {
			event, _ = sjson.SetBytes(event, "b64_json", b64)
			hasImageData = true
		}
		if url := strings.TrimSpace(item.Get("url").String()); url != "" {
			event, _ = sjson.SetBytes(event, "url", url)
			hasImageData = true
		}
		if revisedPrompt := strings.TrimSpace(item.Get("revised_prompt").String()); revisedPrompt != "" {
			event, _ = sjson.SetBytes(event, "revised_prompt", revisedPrompt)
		}
		if !hasImageData {
			continue
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("image response did not include image data")
	}
	return events, nil
}
