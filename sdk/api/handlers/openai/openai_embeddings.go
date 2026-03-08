package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

const geminiGenerativeLanguageBaseURL = "https://generativelanguage.googleapis.com"

type openAIEmbeddingsRequest struct {
	Model          string          `json:"model"`
	Input          json.RawMessage `json:"input"`
	EncodingFormat string          `json:"encoding_format,omitempty"`
	Dimensions     int             `json:"dimensions,omitempty"`
}

type openAIEmbeddingsResponse struct {
	Object string                  `json:"object"`
	Data   []openAIEmbeddingRecord `json:"data"`
	Model  string                  `json:"model"`
	Usage  openAIEmbeddingsUsage   `json:"usage"`
}

type openAIEmbeddingRecord struct {
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

type openAIEmbeddingsUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// Embeddings handles the OpenAI-compatible /v1/embeddings endpoint.
// It currently translates Gemini embedding requests into the OpenAI embeddings shape.
func (h *OpenAIAPIHandler) Embeddings(c *gin.Context) {
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

	req, errMsg := parseOpenAIEmbeddingsRequest(rawJSON)
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		return
	}

	c.Header("Content-Type", "application/json")
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, execErr := h.executeEmbeddings(cliCtx, req)
	stopKeepAlive()
	if execErr != nil {
		h.WriteErrorResponse(c, execErr)
		cliCancel(execErr.Error)
		return
	}

	_, _ = c.Writer.Write(resp)
	cliCancel()
}

func parseOpenAIEmbeddingsRequest(rawJSON []byte) (*openAIEmbeddingsRequest, *interfaces.ErrorMessage) {
	var req openAIEmbeddingsRequest
	if err := json.Unmarshal(rawJSON, &req); err != nil {
		return nil, invalidEmbeddingsRequest(fmt.Sprintf("invalid JSON: %v", err))
	}

	req.Model = normalizeEmbeddingModelName(req.Model)
	if req.Model == "" {
		return nil, invalidEmbeddingsRequest("model is required")
	}

	encodingFormat := strings.ToLower(strings.TrimSpace(req.EncodingFormat))
	switch encodingFormat {
	case "", "float":
		req.EncodingFormat = "float"
	case "base64":
		// The OpenAI SDK can request base64 by default for embeddings.
		// We still return float vectors so existing downstream callers keep working.
		req.EncodingFormat = encodingFormat
	default:
		return nil, invalidEmbeddingsRequest("only encoding_format=float or base64 is supported")
	}

	if len(bytes.TrimSpace(req.Input)) == 0 {
		return nil, invalidEmbeddingsRequest("input is required")
	}

	inputs, err := parseEmbeddingInputs(req.Input)
	if err != nil {
		return nil, invalidEmbeddingsRequest(err.Error())
	}
	if len(inputs) == 0 {
		return nil, invalidEmbeddingsRequest("input must not be empty")
	}

	return &req, nil
}

func parseEmbeddingInputs(raw json.RawMessage) ([]string, error) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}

	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		if len(many) == 0 {
			return nil, fmt.Errorf("input array must not be empty")
		}
		return many, nil
	}

	return nil, fmt.Errorf("input must be a string or an array of strings")
}

func invalidEmbeddingsRequest(message string) *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New(message),
	}
}

func normalizeEmbeddingModelName(model string) string {
	model = strings.TrimSpace(model)
	return strings.TrimPrefix(model, "models/")
}

func (h *OpenAIAPIHandler) executeEmbeddings(ctx context.Context, req *openAIEmbeddingsRequest) ([]byte, *interfaces.ErrorMessage) {
	if h == nil || h.AuthManager == nil {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusInternalServerError,
			Error:      fmt.Errorf("auth manager is unavailable"),
		}
	}

	auth, normalizedModel, errMsg := h.selectEmbeddingAuth(ctx, req.Model)
	if errMsg != nil {
		return nil, errMsg
	}

	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "gemini":
		return h.executeGeminiEmbeddings(ctx, auth, normalizedModel, req)
	case "gemini-cli":
		return h.executeGeminiCLIEmbeddingsViaVertex(ctx, auth, normalizedModel, req)
	default:
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("provider %s does not support /v1/embeddings", auth.Provider),
		}
	}
}

func (h *OpenAIAPIHandler) selectEmbeddingAuth(ctx context.Context, model string) (*coreauth.Auth, string, *interfaces.ErrorMessage) {
	providers, normalizedModel, errMsg := h.GetRequestDetails(model)
	if errMsg == nil {
		auth, err := h.AuthManager.SelectAuthForModel(ctx, providers, normalizedModel, coreexecutor.Options{})
		if err == nil {
			return auth, normalizedModel, nil
		}
		status := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil && se.StatusCode() > 0 {
			status = se.StatusCode()
		}
		return nil, "", &interfaces.ErrorMessage{StatusCode: status, Error: err}
	}

	normalizedModel = normalizeEmbeddingModelName(model)
	if normalizedModel == "gemini-embedding-001" {
		for _, auth := range h.AuthManager.List() {
			if auth == nil || auth.Disabled {
				continue
			}
			provider := strings.ToLower(strings.TrimSpace(auth.Provider))
			if provider == "gemini" || provider == "gemini-cli" {
				return auth, normalizedModel, nil
			}
		}
	}

	return nil, "", errMsg
}

func (h *OpenAIAPIHandler) executeGeminiEmbeddings(ctx context.Context, auth *coreauth.Auth, model string, req *openAIEmbeddingsRequest) ([]byte, *interfaces.ErrorMessage) {
	inputs, err := parseEmbeddingInputs(req.Input)
	if err != nil {
		return nil, invalidEmbeddingsRequest(err.Error())
	}

	endpointURL, payload, err := buildGeminiEmbeddingRequest(auth, model, inputs, req.Dimensions)
	if err != nil {
		return nil, invalidEmbeddingsRequest(err.Error())
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusInternalServerError,
			Error:      err,
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(body))
	if err != nil {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusInternalServerError,
			Error:      err,
		}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := h.AuthManager.HttpRequest(ctx, auth, httpReq)
	if err != nil {
		status := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil && se.StatusCode() > 0 {
			status = se.StatusCode()
		}
		return nil, &interfaces.ErrorMessage{StatusCode: status, Error: err}
	}
	defer func() {
		_ = httpResp.Body.Close()
	}()

	respBody, readErr := io.ReadAll(httpResp.Body)
	if readErr != nil {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusInternalServerError,
			Error:      readErr,
		}
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, &interfaces.ErrorMessage{
			StatusCode: httpResp.StatusCode,
			Error:      errors.New(string(respBody)),
		}
	}

	translated, err := convertGeminiEmbeddingResponseToOpenAI(model, respBody)
	if err != nil {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadGateway,
			Error:      fmt.Errorf("invalid Gemini embeddings response: %w", err),
		}
	}
	return translated, nil
}

func (h *OpenAIAPIHandler) executeGeminiCLIEmbeddingsViaVertex(ctx context.Context, auth *coreauth.Auth, model string, req *openAIEmbeddingsRequest) ([]byte, *interfaces.ErrorMessage) {
	inputs, err := parseEmbeddingInputs(req.Input)
	if err != nil {
		return nil, invalidEmbeddingsRequest(err.Error())
	}

	endpointURL, payload, err := buildVertexEmbeddingRequest(auth, model, inputs, req.Dimensions)
	if err != nil {
		return nil, invalidEmbeddingsRequest(err.Error())
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusInternalServerError,
			Error:      err,
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(body))
	if err != nil {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusInternalServerError,
			Error:      err,
		}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := h.AuthManager.HttpRequest(ctx, auth, httpReq)
	if err != nil {
		status := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil && se.StatusCode() > 0 {
			status = se.StatusCode()
		}
		return nil, &interfaces.ErrorMessage{StatusCode: status, Error: err}
	}
	defer func() {
		_ = httpResp.Body.Close()
	}()

	respBody, readErr := io.ReadAll(httpResp.Body)
	if readErr != nil {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusInternalServerError,
			Error:      readErr,
		}
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, &interfaces.ErrorMessage{
			StatusCode: httpResp.StatusCode,
			Error:      errors.New(string(respBody)),
		}
	}

	translated, err := convertVertexEmbeddingResponseToOpenAI(model, respBody)
	if err != nil {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadGateway,
			Error:      fmt.Errorf("invalid Vertex embeddings response: %w", err),
		}
	}
	return translated, nil
}

func buildGeminiEmbeddingRequest(auth *coreauth.Auth, model string, inputs []string, dimensions int) (string, any, error) {
	model = normalizeEmbeddingModelName(model)
	if model == "" {
		return "", nil, fmt.Errorf("model is required")
	}
	baseURL := resolveGeminiEmbeddingBaseURL(auth)

	escapedModel := url.PathEscape(model)
	if len(inputs) == 1 {
		payload := map[string]any{
			"content": map[string]any{
				"parts": []map[string]string{{"text": inputs[0]}},
			},
		}
		if dimensions > 0 {
			payload["outputDimensionality"] = dimensions
		}
		return fmt.Sprintf("%s/v1beta/models/%s:embedContent", baseURL, escapedModel), payload, nil
	}

	requests := make([]map[string]any, 0, len(inputs))
	for _, input := range inputs {
		entry := map[string]any{
			"model": "models/" + model,
			"content": map[string]any{
				"parts": []map[string]string{{"text": input}},
			},
		}
		if dimensions > 0 {
			entry["outputDimensionality"] = dimensions
		}
		requests = append(requests, entry)
	}

	return fmt.Sprintf("%s/v1beta/models/%s:batchEmbedContents", baseURL, escapedModel), map[string]any{
		"requests": requests,
	}, nil
}

func resolveGeminiEmbeddingBaseURL(auth *coreauth.Auth) string {
	baseURL := geminiGenerativeLanguageBaseURL
	if auth != nil && auth.Attributes != nil {
		if custom := strings.TrimSpace(auth.Attributes["base_url"]); custom != "" {
			baseURL = strings.TrimRight(custom, "/")
		}
	}
	if baseURL == "" {
		return geminiGenerativeLanguageBaseURL
	}
	return baseURL
}

func buildVertexEmbeddingRequest(auth *coreauth.Auth, model string, inputs []string, dimensions int) (string, any, error) {
	projectID := resolveGeminiCLIProjectID(auth)
	if projectID == "" {
		return "", nil, fmt.Errorf("missing Gemini project_id for Vertex embeddings")
	}

	// Vertex exposes text embedding models through PredictionService.
	upstreamModel := "text-embedding-005"
	instances := make([]map[string]any, 0, len(inputs))
	for _, input := range inputs {
		instances = append(instances, map[string]any{"content": input})
	}
	payload := map[string]any{
		"instances": instances,
	}
	if dimensions > 0 {
		payload["parameters"] = map[string]any{"outputDimensionality": dimensions}
	}

	url := fmt.Sprintf(
		"https://us-central1-aiplatform.googleapis.com/v1/projects/%s/locations/us-central1/publishers/google/models/%s:predict",
		url.PathEscape(projectID),
		upstreamModel,
	)
	return url, payload, nil
}

func resolveGeminiCLIProjectID(auth *coreauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if value, ok := auth.Metadata["project_id"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func convertGeminiEmbeddingResponseToOpenAI(model string, rawJSON []byte) ([]byte, error) {
	type geminiEmbeddingValues struct {
		Values []float64 `json:"values"`
	}
	type geminiSingleEmbeddingResponse struct {
		Embedding geminiEmbeddingValues `json:"embedding"`
	}
	type geminiBatchEmbeddingResponse struct {
		Embeddings []geminiEmbeddingValues `json:"embeddings"`
	}

	var out openAIEmbeddingsResponse
	out.Object = "list"
	out.Model = normalizeEmbeddingModelName(model)

	var single geminiSingleEmbeddingResponse
	if err := json.Unmarshal(rawJSON, &single); err == nil && len(single.Embedding.Values) > 0 {
		out.Data = []openAIEmbeddingRecord{{
			Object:    "embedding",
			Embedding: single.Embedding.Values,
			Index:     0,
		}}
		return json.Marshal(out)
	}

	var batch geminiBatchEmbeddingResponse
	if err := json.Unmarshal(rawJSON, &batch); err == nil && len(batch.Embeddings) > 0 {
		out.Data = make([]openAIEmbeddingRecord, 0, len(batch.Embeddings))
		for i, embedding := range batch.Embeddings {
			out.Data = append(out.Data, openAIEmbeddingRecord{
				Object:    "embedding",
				Embedding: embedding.Values,
				Index:     i,
			})
		}
		return json.Marshal(out)
	}

	return nil, fmt.Errorf("missing embedding values")
}

func convertVertexEmbeddingResponseToOpenAI(model string, rawJSON []byte) ([]byte, error) {
	type vertexEmbedding struct {
		Embeddings struct {
			Values []float64 `json:"values"`
		} `json:"embeddings"`
	}
	type vertexResponse struct {
		Predictions []vertexEmbedding `json:"predictions"`
	}

	var response vertexResponse
	if err := json.Unmarshal(rawJSON, &response); err != nil {
		return nil, err
	}
	if len(response.Predictions) == 0 {
		return nil, fmt.Errorf("missing predictions")
	}

	out := openAIEmbeddingsResponse{
		Object: "list",
		Model:  normalizeEmbeddingModelName(model),
	}
	out.Data = make([]openAIEmbeddingRecord, 0, len(response.Predictions))
	for i, prediction := range response.Predictions {
		if len(prediction.Embeddings.Values) == 0 {
			return nil, fmt.Errorf("missing embedding values")
		}
		out.Data = append(out.Data, openAIEmbeddingRecord{
			Object:    "embedding",
			Embedding: prediction.Embeddings.Values,
			Index:     i,
		})
	}
	return json.Marshal(out)
}
