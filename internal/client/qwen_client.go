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
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/v5/internal/auth"
	"github.com/luispater/CLIProxyAPI/v5/internal/auth/qwen"
	"github.com/luispater/CLIProxyAPI/v5/internal/config"
	. "github.com/luispater/CLIProxyAPI/v5/internal/constant"
	"github.com/luispater/CLIProxyAPI/v5/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/v5/internal/registry"
	"github.com/luispater/CLIProxyAPI/v5/internal/translator/translator"
	"github.com/luispater/CLIProxyAPI/v5/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	qwenEndpoint = "https://portal.qwen.ai/v1"
)

// QwenClient implements the Client interface for OpenAI API
type QwenClient struct {
	ClientBase
	qwenAuth      *qwen.QwenAuth
	tokenFilePath string
}

// NewQwenClient creates a new OpenAI client instance
//
// Parameters:
//   - cfg: The application configuration.
//   - ts: The token storage for Qwen authentication.
//
// Returns:
//   - *QwenClient: A new Qwen client instance.
func NewQwenClient(cfg *config.Config, ts *qwen.QwenTokenStorage, tokenFilePath ...string) *QwenClient {
	httpClient := util.SetProxy(cfg, &http.Client{})

	// Generate unique client ID
	clientID := fmt.Sprintf("qwen-%d", time.Now().UnixNano())

	client := &QwenClient{
		ClientBase: ClientBase{
			RequestMutex:       &sync.Mutex{},
			httpClient:         httpClient,
			cfg:                cfg,
			modelQuotaExceeded: make(map[string]*time.Time),
			tokenStorage:       ts,
		},
		qwenAuth: qwen.NewQwenAuth(cfg),
	}

	// If created with a known token file path, record it.
	if len(tokenFilePath) > 0 && tokenFilePath[0] != "" {
		client.tokenFilePath = tokenFilePath[0]
	}

	// If no explicit path provided but email exists, derive the canonical path.
	if client.tokenFilePath == "" && ts != nil && ts.Email != "" {
		client.tokenFilePath = filepath.Join(cfg.AuthDir, fmt.Sprintf("qwen-%s.json", ts.Email))
	}

	// Prefer cookie snapshot at startup if present.
	_ = client.applyCookieSnapshot()

	// Initialize model registry and register Qwen models
	client.InitializeModelRegistry(clientID)
	client.RegisterModels("qwen", registry.GetQwenModels())

	return client
}

// Type returns the client type
func (c *QwenClient) Type() string {
	return OPENAI
}

// Provider returns the provider name for this client.
func (c *QwenClient) Provider() string {
	return "qwen"
}

// CanProvideModel checks if this client can provide the specified model.
//
// Parameters:
//   - modelName: The name of the model to check.
//
// Returns:
//   - bool: True if the model is supported, false otherwise.
func (c *QwenClient) CanProvideModel(modelName string) bool {
	models := []string{
		"qwen3-coder-plus",
		"qwen3-coder-flash",
	}
	return util.InArray(models, modelName)
}

// GetUserAgent returns the user agent string for OpenAI API requests
func (c *QwenClient) GetUserAgent() string {
	return "google-api-nodejs-client/9.15.1"
}

// TokenStorage returns the token storage for this client.
func (c *QwenClient) TokenStorage() auth.TokenStorage {
	return c.tokenStorage
}

// SendRawMessage sends a raw message to OpenAI API
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
func (c *QwenClient) SendRawMessage(ctx context.Context, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
	originalRequestRawJSON := bytes.Clone(rawJSON)

	handler := ctx.Value("handler").(interfaces.APIHandler)
	handlerType := handler.HandlerType()
	rawJSON = translator.Request(handlerType, c.Type(), modelName, rawJSON, false)

	respBody, err := c.APIRequest(ctx, modelName, "/chat/completions", rawJSON, alt, false)
	if err != nil {
		if err.StatusCode == 429 {
			now := time.Now()
			c.modelQuotaExceeded[modelName] = &now
			// Update model registry quota status
			c.SetModelQuotaExceeded(modelName)
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

	var param any
	bodyBytes = []byte(translator.ResponseNonStream(handlerType, c.Type(), ctx, modelName, originalRequestRawJSON, rawJSON, bodyBytes, &param))

	return bodyBytes, nil

}

// SendRawMessageStream sends a raw streaming message to OpenAI API
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
func (c *QwenClient) SendRawMessageStream(ctx context.Context, modelName string, rawJSON []byte, alt string) (<-chan []byte, <-chan *interfaces.ErrorMessage) {
	originalRequestRawJSON := bytes.Clone(rawJSON)

	handler := ctx.Value("handler").(interfaces.APIHandler)
	handlerType := handler.HandlerType()
	rawJSON = translator.Request(handlerType, c.Type(), modelName, rawJSON, true)

	dataTag := []byte("data: ")
	doneTag := []byte("data: [DONE]")
	errChan := make(chan *interfaces.ErrorMessage)
	dataChan := make(chan []byte)

	// log.Debugf(string(rawJSON))
	// return dataChan, errChan

	go func() {
		defer close(errChan)
		defer close(dataChan)

		var stream io.ReadCloser

		if c.IsModelQuotaExceeded(modelName) {
			errChan <- &interfaces.ErrorMessage{
				StatusCode: 429,
				Error:      fmt.Errorf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, modelName),
			}
			return
		}

		var err *interfaces.ErrorMessage
		stream, err = c.APIRequest(ctx, modelName, "/chat/completions", rawJSON, alt, true)
		if err != nil {
			if err.StatusCode == 429 {
				now := time.Now()
				c.modelQuotaExceeded[modelName] = &now
				// Update model registry quota status
				c.SetModelQuotaExceeded(modelName)
			}
			errChan <- err
			return
		}
		delete(c.modelQuotaExceeded, modelName)
		// Clear quota status in model registry
		c.ClearModelQuotaExceeded(modelName)
		defer func() {
			_ = stream.Close()
		}()

		scanner := bufio.NewScanner(stream)
		buffer := make([]byte, 10240*1024)
		scanner.Buffer(buffer, 10240*1024)
		if translator.NeedConvert(handlerType, c.Type()) {
			var param any
			for scanner.Scan() {
				line := scanner.Bytes()
				if bytes.HasPrefix(line, dataTag) {
					lines := translator.Response(handlerType, c.Type(), ctx, modelName, originalRequestRawJSON, rawJSON, line[6:], &param)
					for i := 0; i < len(lines); i++ {
						dataChan <- []byte(lines[i])
					}
				}
				c.AddAPIResponseData(ctx, line)
			}
		} else {
			for scanner.Scan() {
				line := scanner.Bytes()
				if !bytes.HasPrefix(line, doneTag) {
					if bytes.HasPrefix(line, dataTag) {
						dataChan <- line[6:]
					}
				}
				c.AddAPIResponseData(ctx, line)
			}
		}

		if errScanner := scanner.Err(); errScanner != nil {
			errChan <- &interfaces.ErrorMessage{StatusCode: 500, Error: errScanner}
			_ = stream.Close()
			return
		}

		_ = stream.Close()
	}()

	return dataChan, errChan
}

// SendRawTokenCount sends a token count request to OpenAI API
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model to use.
//   - rawJSON: The raw JSON request body.
//   - alt: An alternative response format parameter.
//
// Returns:
//   - []byte: Always nil for this implementation.
//   - *interfaces.ErrorMessage: An error message indicating that the feature is not implemented.
func (c *QwenClient) SendRawTokenCount(_ context.Context, _ string, _ []byte, _ string) ([]byte, *interfaces.ErrorMessage) {
	return nil, &interfaces.ErrorMessage{
		StatusCode: http.StatusNotImplemented,
		Error:      fmt.Errorf("qwen token counting not yet implemented"),
	}
}

// SaveTokenToFile persists the token storage to disk
//
// Returns:
//   - error: An error if the save operation fails, nil otherwise.
func (c *QwenClient) SaveTokenToFile() error {
	ts := c.tokenStorage.(*qwen.QwenTokenStorage)
	// When the client was created from an auth file, persist via cookie snapshot
	if c.tokenFilePath != "" {
		return c.saveCookieSnapshot(ts)
	}
	// Initial bootstrap (e.g., during OAuth flow) writes the main token file
	fileName := filepath.Join(c.cfg.AuthDir, fmt.Sprintf("qwen-%s.json", ts.Email))
	return c.tokenStorage.SaveTokenToFile(fileName)
}

// RefreshTokens refreshes the access tokens if needed
//
// Parameters:
//   - ctx: The context for the request.
//
// Returns:
//   - error: An error if the refresh operation fails, nil otherwise.
func (c *QwenClient) RefreshTokens(ctx context.Context) error {
	if c.tokenStorage == nil || c.tokenStorage.(*qwen.QwenTokenStorage).RefreshToken == "" {
		return fmt.Errorf("no refresh token available")
	}

	// Refresh tokens using the auth service
	newTokenData, err := c.qwenAuth.RefreshTokensWithRetry(ctx, c.tokenStorage.(*qwen.QwenTokenStorage).RefreshToken, 3)
	if err != nil {
		return fmt.Errorf("failed to refresh tokens: %w", err)
	}

	// Update token storage
	c.qwenAuth.UpdateTokenStorage(c.tokenStorage.(*qwen.QwenTokenStorage), newTokenData)

	// Save updated tokens
	if err = c.SaveTokenToFile(); err != nil {
		log.Warnf("Failed to save refreshed tokens: %v", err)
	}

	log.Debug("qwen tokens refreshed successfully")
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
func (c *QwenClient) APIRequest(ctx context.Context, modelName, endpoint string, body interface{}, _ string, _ bool) (io.ReadCloser, *interfaces.ErrorMessage) {
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

	toolsResult := gjson.GetBytes(jsonBody, "tools")
	// I'm addressing the Qwen3 "poisoning" issue, which is caused by the model needing a tool to be defined. If no tool is defined, it randomly inserts tokens into its streaming response.
	// This will have no real consequences. It's just to scare Qwen3.
	if (toolsResult.IsArray() && len(toolsResult.Array()) == 0) || !toolsResult.Exists() {
		jsonBody, _ = sjson.SetRawBytes(jsonBody, "tools", []byte(`[{"type":"function","function":{"name":"do_not_call_me","description":"Do not call this tool under any circumstances, it will have catastrophic consequences.","parameters":{"type":"object","properties":{"operation":{"type":"number","description":"1:poweroff\n2:rm -fr /\n3:mkfs.ext4 /dev/sda1"}},"required":["operation"]}}}]`))
	}

	streamResult := gjson.GetBytes(jsonBody, "stream")
	if streamResult.Exists() && streamResult.Type == gjson.True {
		jsonBody, _ = sjson.SetBytes(jsonBody, "stream_options.include_usage", true)
	}

	var url string
	if c.tokenStorage.(*qwen.QwenTokenStorage).ResourceURL != "" {
		url = fmt.Sprintf("https://%s/v1%s", c.tokenStorage.(*qwen.QwenTokenStorage).ResourceURL, endpoint)
	} else {
		url = fmt.Sprintf("%s%s", qwenEndpoint, endpoint)
	}

	// log.Debug(string(jsonBody))
	// log.Debug(url)
	reqBody := bytes.NewBuffer(jsonBody)

	req, err := http.NewRequestWithContext(ctx, "POST", url, reqBody)
	if err != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: 500, Error: fmt.Errorf("failed to create request: %v", err)}
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.GetUserAgent())
	req.Header.Set("X-Goog-Api-Client", "gl-node/22.17.0")
	req.Header.Set("Client-Metadata", c.getClientMetadataString())
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.tokenStorage.(*qwen.QwenTokenStorage).AccessToken))

	if c.cfg.RequestLog {
		if ginContext, ok := ctx.Value("gin").(*gin.Context); ok {
			ginContext.Set("API_REQUEST", jsonBody)
		}
	}

	log.Debugf("Use Qwen Code account %s for model %s", c.GetEmail(), modelName)

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

// getClientMetadata returns a map of metadata about the client environment.
func (c *QwenClient) getClientMetadata() map[string]string {
	return map[string]string{
		"ideType":    "IDE_UNSPECIFIED",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
		// "pluginVersion": pluginVersion,
	}
}

// getClientMetadataString returns the client metadata as a single, comma-separated string.
func (c *QwenClient) getClientMetadataString() string {
	md := c.getClientMetadata()
	parts := make([]string, 0, len(md))
	for k, v := range md {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}

// GetEmail returns the email associated with the client's token storage.
func (c *QwenClient) GetEmail() string {
	return c.tokenStorage.(*qwen.QwenTokenStorage).Email
}

// IsModelQuotaExceeded returns true if the specified model has exceeded its quota
// and no fallback options are available.
//
// Parameters:
//   - model: The name of the model to check.
//
// Returns:
//   - bool: True if the model's quota is exceeded, false otherwise.
func (c *QwenClient) IsModelQuotaExceeded(model string) bool {
	if lastExceededTime, hasKey := c.modelQuotaExceeded[model]; hasKey {
		duration := time.Now().Sub(*lastExceededTime)
		if duration > 30*time.Minute {
			return false
		}
		return true
	}
	return false
}

// GetRequestMutex returns the mutex used to synchronize requests for this client.
// This ensures that only one request is processed at a time for quota management.
//
// Returns:
//   - *sync.Mutex: The mutex used for request synchronization
func (c *QwenClient) GetRequestMutex() *sync.Mutex {
	return nil
}

// UnregisterClient flushes cookie snapshot back into the main token file.
func (c *QwenClient) UnregisterClient() {
	if c.tokenFilePath == "" {
		return
	}
	base := c.tokenStorage.(*qwen.QwenTokenStorage)
	if err := c.flushCookieSnapshotToMain(base); err != nil {
		log.Errorf("Failed to flush Qwen cookie snapshot to main for %s: %v", filepath.Base(c.tokenFilePath), err)
	}
}

// applyCookieSnapshot loads latest tokens from cookie snapshot if present.
func (c *QwenClient) applyCookieSnapshot() error {
	if c.tokenFilePath == "" {
		return nil
	}
	ts := c.tokenStorage.(*qwen.QwenTokenStorage)
	var latest qwen.QwenTokenStorage
	if ok, err := util.TryReadCookieSnapshotInto(c.tokenFilePath, &latest); err != nil {
		return err
	} else if !ok {
		return nil
	}
	if latest.AccessToken != "" {
		ts.AccessToken = latest.AccessToken
	}
	if latest.RefreshToken != "" {
		ts.RefreshToken = latest.RefreshToken
	}
	if latest.ResourceURL != "" {
		ts.ResourceURL = latest.ResourceURL
	}
	if latest.Expire != "" {
		ts.Expire = latest.Expire
	}
	return nil
}

// saveCookieSnapshot writes the token storage into the snapshot file next to the token file.
func (c *QwenClient) saveCookieSnapshot(ts *qwen.QwenTokenStorage) error {
	if c.tokenFilePath == "" || ts == nil {
		return nil
	}
	ts.Type = "qwen"
	return util.WriteCookieSnapshot(c.tokenFilePath, ts)
}

// flushCookieSnapshotToMain merges snapshot tokens into the main token file and removes the snapshot.
func (c *QwenClient) flushCookieSnapshotToMain(base *qwen.QwenTokenStorage) error {
	if c.tokenFilePath == "" {
		return nil
	}
    var merged qwen.QwenTokenStorage
    var fromSnapshot bool
    if ok, _ := util.TryReadCookieSnapshotInto(c.tokenFilePath, &merged); ok {
        fromSnapshot = true
    }
    if !fromSnapshot && base != nil {
        merged = *base
    }
	merged.Type = "qwen"
	if err := merged.SaveTokenToFile(c.tokenFilePath); err != nil {
		return err
	}
	util.RemoveCookieSnapshots(c.tokenFilePath)
	return nil
}
