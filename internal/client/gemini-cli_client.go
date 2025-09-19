// Package client defines the interface and base structure for AI API clients.
// It provides a common interface that all supported AI service clients must implement,
// including methods for sending messages, handling streams, and managing authentication.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	geminiAuth "github.com/luispater/CLIProxyAPI/v5/internal/auth/gemini"
	"github.com/luispater/CLIProxyAPI/v5/internal/config"
	. "github.com/luispater/CLIProxyAPI/v5/internal/constant"
	"github.com/luispater/CLIProxyAPI/v5/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/v5/internal/registry"
	"github.com/luispater/CLIProxyAPI/v5/internal/translator/translator"
	"github.com/luispater/CLIProxyAPI/v5/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/oauth2"
)

const (
	codeAssistEndpoint = "https://cloudcode-pa.googleapis.com"
	apiVersion         = "v1internal"
)

var (
	previewModels = map[string][]string{
		"gemini-2.5-pro":        {"gemini-2.5-pro-preview-05-06", "gemini-2.5-pro-preview-06-05"},
		"gemini-2.5-flash":      {"gemini-2.5-flash-preview-04-17", "gemini-2.5-flash-preview-05-20"},
		"gemini-2.5-flash-lite": {"gemini-2.5-flash-lite-preview-06-17"},
	}
)

// GeminiCLIClient is the main client for interacting with the CLI API.
type GeminiCLIClient struct {
	ClientBase
}

// NewGeminiCLIClient creates a new CLI API client.
//
// Parameters:
//   - httpClient: The HTTP client to use for requests.
//   - ts: The token storage for Gemini authentication.
//   - cfg: The application configuration.
//
// Returns:
//   - *GeminiCLIClient: A new Gemini CLI client instance.
func NewGeminiCLIClient(httpClient *http.Client, ts *geminiAuth.GeminiTokenStorage, cfg *config.Config) *GeminiCLIClient {
	// Generate unique client ID
	clientID := fmt.Sprintf("gemini-cli-%d", time.Now().UnixNano())

	client := &GeminiCLIClient{
		ClientBase: ClientBase{
			RequestMutex:       &sync.Mutex{},
			httpClient:         httpClient,
			cfg:                cfg,
			tokenStorage:       ts,
			modelQuotaExceeded: make(map[string]*time.Time),
		},
	}

	// Initialize model registry and register Gemini models
	client.InitializeModelRegistry(clientID)
	client.RegisterModels("gemini-cli", registry.GetGeminiCLIModels())

	return client
}

// Type returns the client type
func (c *GeminiCLIClient) Type() string {
	return GEMINICLI
}

// Provider returns the provider name for this client.
func (c *GeminiCLIClient) Provider() string {
	return GEMINICLI
}

// CanProvideModel checks if this client can provide the specified model.
//
// Parameters:
//   - modelName: The name of the model to check.
//
// Returns:
//   - bool: True if the model is supported, false otherwise.
func (c *GeminiCLIClient) CanProvideModel(modelName string) bool {
	models := []string{
		"gemini-2.5-pro",
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
	}
	return util.InArray(models, modelName)
}

// SetProjectID updates the project ID for the client's token storage.
//
// Parameters:
//   - projectID: The new project ID.
func (c *GeminiCLIClient) SetProjectID(projectID string) {
	c.tokenStorage.(*geminiAuth.GeminiTokenStorage).ProjectID = projectID
}

// SetIsAuto configures whether the client should operate in automatic mode.
//
// Parameters:
//   - auto: A boolean indicating if automatic mode should be enabled.
func (c *GeminiCLIClient) SetIsAuto(auto bool) {
	c.tokenStorage.(*geminiAuth.GeminiTokenStorage).Auto = auto
}

// SetIsChecked sets the checked status for the client's token storage.
//
// Parameters:
//   - checked: A boolean indicating if the token storage has been checked.
func (c *GeminiCLIClient) SetIsChecked(checked bool) {
	c.tokenStorage.(*geminiAuth.GeminiTokenStorage).Checked = checked
}

// IsChecked returns whether the client's token storage has been checked.
func (c *GeminiCLIClient) IsChecked() bool {
	return c.tokenStorage.(*geminiAuth.GeminiTokenStorage).Checked
}

// IsAuto returns whether the client is operating in automatic mode.
func (c *GeminiCLIClient) IsAuto() bool {
	return c.tokenStorage.(*geminiAuth.GeminiTokenStorage).Auto
}

// GetEmail returns the email address associated with the client's token storage.
func (c *GeminiCLIClient) GetEmail() string {
	return c.tokenStorage.(*geminiAuth.GeminiTokenStorage).Email
}

// GetProjectID returns the Google Cloud project ID from the client's token storage.
func (c *GeminiCLIClient) GetProjectID() string {
	if c.tokenStorage != nil {
		if ts, ok := c.tokenStorage.(*geminiAuth.GeminiTokenStorage); ok {
			return ts.ProjectID
		}
	}
	return ""
}

// SetupUser performs the initial user onboarding and setup.
//
// Parameters:
//   - ctx: The context for the request.
//   - email: The user's email address.
//   - projectID: The Google Cloud project ID.
//
// Returns:
//   - error: An error if the setup fails, nil otherwise.
func (c *GeminiCLIClient) SetupUser(ctx context.Context, email, projectID string) error {
	c.tokenStorage.(*geminiAuth.GeminiTokenStorage).Email = email
	log.Info("Performing user onboarding...")

	// 1. LoadCodeAssist
	loadAssistReqBody := map[string]interface{}{
		"metadata": c.getClientMetadata(),
	}
	if projectID != "" {
		loadAssistReqBody["cloudaicompanionProject"] = projectID
	}

	var loadAssistResp map[string]interface{}
	err := c.makeAPIRequest(ctx, "loadCodeAssist", "POST", loadAssistReqBody, &loadAssistResp)
	if err != nil {
		return fmt.Errorf("failed to load code assist: %w", err)
	}

	// 2. OnboardUser
	var onboardTierID = "legacy-tier"
	if tiers, ok := loadAssistResp["allowedTiers"].([]interface{}); ok {
		for _, t := range tiers {
			if tier, tierOk := t.(map[string]interface{}); tierOk {
				if isDefault, isDefaultOk := tier["isDefault"].(bool); isDefaultOk && isDefault {
					if id, idOk := tier["id"].(string); idOk {
						onboardTierID = id
						break
					}
				}
			}
		}
	}

	onboardProjectID := projectID
	if p, ok := loadAssistResp["cloudaicompanionProject"].(string); ok && p != "" {
		onboardProjectID = p
	}

	onboardReqBody := map[string]interface{}{
		"tierId":   onboardTierID,
		"metadata": c.getClientMetadata(),
	}
	if onboardProjectID != "" {
		onboardReqBody["cloudaicompanionProject"] = onboardProjectID
	} else {
		return fmt.Errorf("failed to start user onboarding, need define a project id")
	}

	for {
		var lroResp map[string]interface{}
		err = c.makeAPIRequest(ctx, "onboardUser", "POST", onboardReqBody, &lroResp)
		if err != nil {
			return fmt.Errorf("failed to start user onboarding: %w", err)
		}
		// a, _ := json.Marshal(&lroResp)
		// log.Debug(string(a))

		// 3. Poll Long-Running Operation (LRO)
		done, doneOk := lroResp["done"].(bool)
		if doneOk && done {
			if project, projectOk := lroResp["response"].(map[string]interface{})["cloudaicompanionProject"].(map[string]interface{}); projectOk {
				if projectID != "" {
					c.tokenStorage.(*geminiAuth.GeminiTokenStorage).ProjectID = projectID
				} else {
					c.tokenStorage.(*geminiAuth.GeminiTokenStorage).ProjectID = project["id"].(string)
				}
				log.Infof("Onboarding complete. Using Project ID: %s", c.tokenStorage.(*geminiAuth.GeminiTokenStorage).ProjectID)
				return nil
			}
		} else {
			log.Println("Onboarding in progress, waiting 5 seconds...")
			time.Sleep(5 * time.Second)
		}
	}
}

// makeAPIRequest handles making requests to the CLI API endpoints.
//
// Parameters:
//   - ctx: The context for the request.
//   - endpoint: The API endpoint to call.
//   - method: The HTTP method to use.
//   - body: The request body.
//   - result: A pointer to a variable to store the response.
//
// Returns:
//   - error: An error if the request fails, nil otherwise.
func (c *GeminiCLIClient) makeAPIRequest(ctx context.Context, endpoint, method string, body interface{}, result interface{}) error {
	var reqBody io.Reader
	var jsonBody []byte
	var err error
	if body != nil {
		jsonBody, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonBody)
	}

	url := fmt.Sprintf("%s/%s:%s", codeAssistEndpoint, apiVersion, endpoint)
	if strings.HasPrefix(endpoint, "operations/") {
		url = fmt.Sprintf("%s/%s", codeAssistEndpoint, endpoint)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	token, err := c.httpClient.Transport.(*oauth2.Transport).Source.Token()
	if err != nil {
		return fmt.Errorf("failed to get token: %w", err)
	}

	// Set headers
	metadataStr := c.getClientMetadataString()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.GetUserAgent())
	req.Header.Set("X-Goog-Api-Client", "gl-node/22.17.0")
	req.Header.Set("Client-Metadata", metadataStr)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))

	if ginContext, ok := ctx.Value("gin").(*gin.Context); ok {
		ginContext.Set("API_REQUEST", jsonBody)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer func() {
		if err = resp.Body.Close(); err != nil {
			log.Printf("warn: failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("api request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	if result != nil {
		if err = json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("failed to decode response body: %w", err)
		}
	}

	return nil
}

// APIRequest handles making requests to the CLI API endpoints.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model to use.
//   - endpoint: The API endpoint to call.
//   - body: The request body.
//   - alt: An alternative response format parameter.
//   - stream: A boolean indicating if the request is for a streaming response.
//
// Returns:
//   - io.ReadCloser: The response body reader.
//   - *interfaces.ErrorMessage: An error message if the request fails.
func (c *GeminiCLIClient) APIRequest(ctx context.Context, modelName, endpoint string, body interface{}, alt string, stream bool) (io.ReadCloser, *interfaces.ErrorMessage) {
	var jsonBody []byte
	var err error
	if byteBody, ok := body.([]byte); ok {
		jsonBody = byteBody
	} else {
		jsonBody, err = json.Marshal(body)
		if err != nil {
			return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: fmt.Errorf("failed to marshal request body: %w", err)}
		}
	}

	var url string
	// Add alt=sse for streaming
	url = fmt.Sprintf("%s/%s:%s", codeAssistEndpoint, apiVersion, endpoint)
	if alt == "" && stream {
		url = url + "?alt=sse"
	} else {
		if alt != "" {
			url = url + fmt.Sprintf("?$alt=%s", alt)
		}
	}

	// log.Debug(string(jsonBody))
	// log.Debug(url)
	reqBody := bytes.NewBuffer(jsonBody)

	req, err := http.NewRequestWithContext(ctx, "POST", url, reqBody)
	if err != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: fmt.Errorf("failed to create request: %v", err)}
	}

	// Set headers
	metadataStr := c.getClientMetadataString()
	req.Header.Set("Content-Type", "application/json")
	token, errToken := c.httpClient.Transport.(*oauth2.Transport).Source.Token()
	if errToken != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: fmt.Errorf("failed to get token: %v", errToken)}
	}
	req.Header.Set("User-Agent", c.GetUserAgent())
	req.Header.Set("X-Goog-Api-Client", "gl-node/22.17.0")
	req.Header.Set("Client-Metadata", metadataStr)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))

	if c.cfg.RequestLog {
		if ginContext, ok := ctx.Value("gin").(*gin.Context); ok {
			ginContext.Set("API_REQUEST", jsonBody)
		}
	}

	log.Debugf("Use Gemini CLI account %s (project id: %s) for model %s", c.GetEmail(), c.GetProjectID(), modelName)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: fmt.Errorf("failed to execute request: %v", err)}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() {
			if err = resp.Body.Close(); err != nil {
				log.Printf("warn: failed to close response body: %v", err)
			}
		}()
		bodyBytes, _ := io.ReadAll(resp.Body)
		// log.Debug(string(jsonBody))
		return nil, &interfaces.ErrorMessage{StatusCode: resp.StatusCode, Error: fmt.Errorf("%s", string(bodyBytes))}
	}

	return resp.Body, nil
}

// SendRawTokenCount handles a token count.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model to use.
//   - rawJSON: The raw JSON request body.
//   - alt: An alternative response format parameter.
//
// Returns:
//   - []byte: The response body.
//   - *interfaces.ErrorMessage: An error message if the request fails.
func (c *GeminiCLIClient) SendRawTokenCount(ctx context.Context, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
	originalRequestRawJSON := bytes.Clone(rawJSON)
	for {
		if c.isModelQuotaExceeded(modelName) {
			if c.cfg.QuotaExceeded.SwitchPreviewModel {
				newModelName := c.getPreviewModel(modelName)
				if newModelName != "" {
					log.Debugf("Model %s is quota exceeded. Switch to preview model %s", modelName, newModelName)
					rawJSON, _ = sjson.SetBytes(rawJSON, "model", newModelName)
					modelName = newModelName
					continue
				}
			}
			return nil, &interfaces.ErrorMessage{
				StatusCode: 429,
				Error:      fmt.Errorf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, modelName),
			}
		}

		handler := ctx.Value("handler").(interfaces.APIHandler)
		handlerType := handler.HandlerType()
		rawJSON = translator.Request(handlerType, c.Type(), modelName, rawJSON, false)
		// Remove project and model from the request body
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "project")
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "model")

		respBody, err := c.APIRequest(ctx, modelName, "countTokens", rawJSON, alt, false)
		if err != nil {
			if err.StatusCode == 429 {
				now := time.Now()
				c.modelQuotaExceeded[modelName] = &now
				// Update model registry quota status
				c.SetModelQuotaExceeded(modelName)
				if c.cfg.QuotaExceeded.SwitchPreviewModel {
					continue
				}
			}
			return nil, err
		}
		delete(c.modelQuotaExceeded, modelName)
		// Clear quota status in model registry
		c.ClearModelQuotaExceeded(modelName)
		bodyBytes, errReadAll := io.ReadAll(respBody)
		if errReadAll != nil {
			return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: errReadAll}
		}

		c.AddAPIResponseData(ctx, bodyBytes)
		var param any
		bodyBytes = []byte(translator.ResponseNonStream(handlerType, c.Type(), ctx, modelName, originalRequestRawJSON, rawJSON, bodyBytes, &param))

		return bodyBytes, nil
	}
}

// SendRawMessage handles a single conversational turn, including tool calls.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model to use.
//   - rawJSON: The raw JSON request body.
//   - alt: An alternative response format parameter.
//
// Returns:
//   - []byte: The response body.
//   - *interfaces.ErrorMessage: An error message if the request fails.
func (c *GeminiCLIClient) SendRawMessage(ctx context.Context, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
	originalRequestRawJSON := bytes.Clone(rawJSON)

	handler := ctx.Value("handler").(interfaces.APIHandler)
	handlerType := handler.HandlerType()
	rawJSON = translator.Request(handlerType, c.Type(), modelName, rawJSON, false)
	rawJSON, _ = sjson.SetBytes(rawJSON, "project", c.GetProjectID())
	rawJSON, _ = sjson.SetBytes(rawJSON, "model", modelName)

	for {
		if c.isModelQuotaExceeded(modelName) {
			if c.cfg.QuotaExceeded.SwitchPreviewModel {
				newModelName := c.getPreviewModel(modelName)
				if newModelName != "" {
					log.Debugf("Model %s is quota exceeded. Switch to preview model %s", modelName, newModelName)
					rawJSON, _ = sjson.SetBytes(rawJSON, "model", newModelName)
					modelName = newModelName
					continue
				}
			}
			return nil, &interfaces.ErrorMessage{
				StatusCode: 429,
				Error:      fmt.Errorf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, modelName),
			}
		}

		respBody, err := c.APIRequest(ctx, modelName, "generateContent", rawJSON, alt, false)
		if err != nil {
			if err.StatusCode == 429 {
				now := time.Now()
				c.modelQuotaExceeded[modelName] = &now
				// Update model registry quota status
				c.SetModelQuotaExceeded(modelName)
				if c.cfg.QuotaExceeded.SwitchPreviewModel {
					continue
				}
			}
			return nil, err
		}
		delete(c.modelQuotaExceeded, modelName)
		// Clear quota status in model registry
		c.ClearModelQuotaExceeded(modelName)
		bodyBytes, errReadAll := io.ReadAll(respBody)
		if errReadAll != nil {
			return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: errReadAll}
		}

		_ = respBody.Close()
		c.AddAPIResponseData(ctx, bodyBytes)

		newCtx := context.WithValue(ctx, "alt", alt)
		var param any
		bodyBytes = []byte(translator.ResponseNonStream(handlerType, c.Type(), newCtx, modelName, originalRequestRawJSON, rawJSON, bodyBytes, &param))

		return bodyBytes, nil
	}
}

// SendRawMessageStream handles a single conversational turn, including tool calls.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model to use.
//   - rawJSON: The raw JSON request body.
//   - alt: An alternative response format parameter.
//
// Returns:
//   - <-chan []byte: A channel for receiving response data chunks.
//   - <-chan *interfaces.ErrorMessage: A channel for receiving error messages.
func (c *GeminiCLIClient) SendRawMessageStream(ctx context.Context, modelName string, rawJSON []byte, alt string) (<-chan []byte, <-chan *interfaces.ErrorMessage) {
	originalRequestRawJSON := bytes.Clone(rawJSON)

	handler := ctx.Value("handler").(interfaces.APIHandler)
	handlerType := handler.HandlerType()
	rawJSON = translator.Request(handlerType, c.Type(), modelName, rawJSON, true)

	rawJSON, _ = sjson.SetBytes(rawJSON, "project", c.GetProjectID())
	rawJSON, _ = sjson.SetBytes(rawJSON, "model", modelName)

	dataTag := []byte("data: ")
	errChan := make(chan *interfaces.ErrorMessage)
	dataChan := make(chan []byte)
	// log.Debugf(string(rawJSON))
	// return dataChan, errChan
	go func() {
		defer close(errChan)
		defer close(dataChan)

		rawJSON, _ = sjson.SetBytes(rawJSON, "project", c.GetProjectID())

		var stream io.ReadCloser
		for {
			if c.isModelQuotaExceeded(modelName) {
				if c.cfg.QuotaExceeded.SwitchPreviewModel {
					newModelName := c.getPreviewModel(modelName)
					if newModelName != "" {
						log.Debugf("Model %s is quota exceeded. Switch to preview model %s", modelName, newModelName)
						rawJSON, _ = sjson.SetBytes(rawJSON, "model", newModelName)
						modelName = newModelName
						continue
					}
				}
				errChan <- &interfaces.ErrorMessage{
					StatusCode: 429,
					Error:      fmt.Errorf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, modelName),
				}
				return
			}

			var err *interfaces.ErrorMessage
			stream, err = c.APIRequest(ctx, modelName, "streamGenerateContent", rawJSON, alt, true)
			if err != nil {
				if err.StatusCode == 429 {
					now := time.Now()
					c.modelQuotaExceeded[modelName] = &now
					// Update model registry quota status
					c.SetModelQuotaExceeded(modelName)
					if c.cfg.QuotaExceeded.SwitchPreviewModel {
						continue
					}
				}
				errChan <- err
				return
			}
			delete(c.modelQuotaExceeded, modelName)
			// Clear quota status in model registry
			c.ClearModelQuotaExceeded(modelName)
			break
		}
		defer func() {
			if stream != nil {
				_ = stream.Close()
			}
		}()

		newCtx := context.WithValue(ctx, "alt", alt)
		var param any
		if alt == "" {
			scanner := bufio.NewScanner(stream)

			if translator.NeedConvert(handlerType, c.Type()) {
				for scanner.Scan() {
					line := scanner.Bytes()
					if bytes.HasPrefix(line, dataTag) {
						lines := translator.Response(handlerType, c.Type(), newCtx, modelName, originalRequestRawJSON, rawJSON, line[6:], &param)
						for i := 0; i < len(lines); i++ {
							dataChan <- []byte(lines[i])
						}
					}
					c.AddAPIResponseData(ctx, line)
				}
			} else {
				for scanner.Scan() {
					line := scanner.Bytes()
					if bytes.HasPrefix(line, dataTag) {
						dataChan <- line[6:]
					}
					c.AddAPIResponseData(ctx, line)
				}
			}

			if errScanner := scanner.Err(); errScanner != nil {
				errChan <- &interfaces.ErrorMessage{StatusCode: 500, Error: errScanner}
				_ = stream.Close()
				return
			}

		} else {
			data, err := io.ReadAll(stream)
			if err != nil {
				errChan <- &interfaces.ErrorMessage{StatusCode: 500, Error: err}
				_ = stream.Close()
				return
			}

			if translator.NeedConvert(handlerType, c.Type()) {
				lines := translator.Response(handlerType, c.Type(), newCtx, modelName, originalRequestRawJSON, rawJSON, data, &param)
				for i := 0; i < len(lines); i++ {
					dataChan <- []byte(lines[i])
				}
			} else {
				dataChan <- data
			}
			c.AddAPIResponseData(ctx, data)
		}

		if translator.NeedConvert(handlerType, c.Type()) {
			lines := translator.Response(handlerType, c.Type(), ctx, modelName, rawJSON, originalRequestRawJSON, []byte("[DONE]"), &param)
			for i := 0; i < len(lines); i++ {
				dataChan <- []byte(lines[i])
			}
		}

		_ = stream.Close()

	}()

	return dataChan, errChan
}

// isModelQuotaExceeded checks if the specified model has exceeded its quota
// within the last 30 minutes.
//
// Parameters:
//   - model: The name of the model to check.
//
// Returns:
//   - bool: True if the model's quota is exceeded, false otherwise.
func (c *GeminiCLIClient) isModelQuotaExceeded(model string) bool {
	if lastExceededTime, hasKey := c.modelQuotaExceeded[model]; hasKey {
		duration := time.Now().Sub(*lastExceededTime)
		if duration > 30*time.Minute {
			return false
		}
		return true
	}
	return false
}

// getPreviewModel returns an available preview model for the given base model,
// or an empty string if no preview models are available or all are quota exceeded.
//
// Parameters:
//   - model: The base model name.
//
// Returns:
//   - string: The name of the preview model to use, or an empty string.
func (c *GeminiCLIClient) getPreviewModel(model string) string {
	if models, hasKey := previewModels[model]; hasKey {
		for i := 0; i < len(models); i++ {
			if !c.isModelQuotaExceeded(models[i]) {
				return models[i]
			}
		}
	}
	return ""
}

// IsModelQuotaExceeded returns true if the specified model has exceeded its quota
// and no fallback options are available.
//
// Parameters:
//   - model: The name of the model to check.
//
// Returns:
//   - bool: True if the model's quota is exceeded, false otherwise.
func (c *GeminiCLIClient) IsModelQuotaExceeded(model string) bool {
	if c.isModelQuotaExceeded(model) {
		if c.cfg.QuotaExceeded.SwitchPreviewModel {
			return c.getPreviewModel(model) == ""
		}
		return true
	}
	return false
}

// CheckCloudAPIIsEnabled sends a simple test request to the API to verify
// that the Cloud AI API is enabled for the user's project. It provides
// an activation URL if the API is disabled.
//
// Returns:
//   - bool: True if the API is enabled, false otherwise.
//   - error: An error if the request fails, nil otherwise.
func (c *GeminiCLIClient) CheckCloudAPIIsEnabled() (bool, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		c.RequestMutex.Unlock()
		cancel()
	}()
	c.RequestMutex.Lock()

	// A simple request to test the API endpoint.
	requestBody := fmt.Sprintf(`{"project":"%s","request":{"contents":[{"role":"user","parts":[{"text":"Be concise. What is the capital of France?"}]}],"generationConfig":{"thinkingConfig":{"include_thoughts":false,"thinkingBudget":0}}},"model":"gemini-2.5-flash"}`, c.tokenStorage.(*geminiAuth.GeminiTokenStorage).ProjectID)

	stream, err := c.APIRequest(ctx, "gemini-2.5-flash", "streamGenerateContent", []byte(requestBody), "", true)
	if err != nil {
		// If a 403 Forbidden error occurs, it likely means the API is not enabled.
		if err.StatusCode == 403 {
			errJSON := err.Error.Error()
			// Check for a specific error code and extract the activation URL.
			if gjson.Get(errJSON, "0.error.code").Int() == 403 {
				activationURL := gjson.Get(errJSON, "0.error.details.0.metadata.activationUrl").String()
				if activationURL != "" {
					log.Warnf(
						"\n\nPlease activate your account with this url:\n\n%s\n\n And execute this command again:\n%s --login --project_id %s",
						activationURL,
						os.Args[0],
						c.tokenStorage.(*geminiAuth.GeminiTokenStorage).ProjectID,
					)
				}
			}
			log.Warnf("\n\nPlease copy this message and create an issue.\n\n%s\n\n", errJSON)
			return false, nil
		}
		return false, err.Error
	}
	defer func() {
		_ = stream.Close()
	}()

	// We only need to know if the request was successful, so we can drain the stream.
	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		// Do nothing, just consume the stream.
	}

	return scanner.Err() == nil, scanner.Err()
}

// GetProjectList fetches a list of Google Cloud projects accessible by the user.
//
// Parameters:
//   - ctx: The context for the request.
//
// Returns:
//   - *interfaces.GCPProject: A list of GCP projects.
//   - error: An error if the request fails, nil otherwise.
func (c *GeminiCLIClient) GetProjectList(ctx context.Context) (*interfaces.GCPProject, error) {
	token, err := c.httpClient.Transport.(*oauth2.Transport).Source.Token()
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://cloudresourcemanager.googleapis.com/v1/projects", nil)
	if err != nil {
		return nil, fmt.Errorf("could not create project list request: %v", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute project list request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("project list request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var project interfaces.GCPProject
	if err = json.NewDecoder(resp.Body).Decode(&project); err != nil {
		return nil, fmt.Errorf("failed to unmarshal project list: %w", err)
	}
	return &project, nil
}

// SaveTokenToFile serializes the client's current token storage to a JSON file.
// The filename is constructed from the user's email and project ID.
//
// Returns:
//   - error: An error if the save operation fails, nil otherwise.
func (c *GeminiCLIClient) SaveTokenToFile() error {
	fileName := filepath.Join(c.cfg.AuthDir, fmt.Sprintf("%s-%s.json", c.tokenStorage.(*geminiAuth.GeminiTokenStorage).Email, c.tokenStorage.(*geminiAuth.GeminiTokenStorage).ProjectID))
	return c.tokenStorage.SaveTokenToFile(fileName)
}

// getClientMetadata returns a map of metadata about the client environment,
// such as IDE type, platform, and plugin version.
func (c *GeminiCLIClient) getClientMetadata() map[string]string {
	return map[string]string{
		"ideType":    "IDE_UNSPECIFIED",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
		// "pluginVersion": pluginVersion,
	}
}

// getClientMetadataString returns the client metadata as a single,
// comma-separated string, which is required for the 'GeminiClient-Metadata' header.
func (c *GeminiCLIClient) getClientMetadataString() string {
	md := c.getClientMetadata()
	parts := make([]string, 0, len(md))
	for k, v := range md {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}

// GetUserAgent constructs the User-Agent string for HTTP requests.
func (c *GeminiCLIClient) GetUserAgent() string {
	// return fmt.Sprintf("GeminiCLI/%s (%s; %s)", pluginVersion, runtime.GOOS, runtime.GOARCH)
	return "google-api-nodejs-client/9.15.1"
}

// GetRequestMutex returns the mutex used to synchronize requests for this client.
// This ensures that only one request is processed at a time for quota management.
//
// Returns:
//   - *sync.Mutex: The mutex used for request synchronization
func (c *GeminiCLIClient) GetRequestMutex() *sync.Mutex {
	return nil
}

func (c *GeminiCLIClient) RefreshTokens(ctx context.Context) error {
	// API keys don't need refreshing
	return nil
}
