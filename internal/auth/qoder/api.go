package qoder

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

const (
	// QoderInferURL is the base URL for Qoder inference API
	QoderInferURL = "https://api1.qoder.sh"
	// QoderSigPath is the signing path for COSY authentication
	QoderSigPath = "/api/v2/service/pro/sse/agent_chat_generation"
	// QoderChatURL is the full URL for chat API
	QoderChatURL = QoderInferURL + "/algo" + QoderSigPath + "?AgentId=agent_common"
)

// ModelMap maps display model names to internal Qoder model keys
var ModelMap = map[string]string{
	"auto":                 "auto",
	"ultimate":             "ultimate",
	"performance":          "performance",
	"qwen-coder-qoder-1.0": "qmodel",
	"qwen3.5-plus":         "q35model",
	"glm-5":                "gmodel",
	"kimi-k2.5":            "kmodel",
	"minimax-m2.7":         "mmodel",
}

// ChatMessage represents a single message in the chat conversation
type ChatMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// ChatRequest represents the request body for chat API
type ChatRequest struct {
	Messages    []ChatMessage `json:"messages"`
	Model       string        `json:"model,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream"`
}

// ChatResponse represents a streaming response chunk
type ChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int `json:"index"`
		Delta        struct {
			Role    string `json:"role,omitempty"`
			Content string `json:"content,omitempty"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// QoderAPI manages API calls to the Qoder cloud
type QoderAPI struct {
	httpClient  *http.Client
	token       string
	userID      string
	name        string
	email       string
	machineID   string
	cliVersion  string
	machineOS   string
}

// NewQoderAPI creates a new QoderAPI instance
func NewQoderAPI(cfg *config.Config, token, userID, name, email, machineID string) *QoderAPI {
	return &QoderAPI{
		httpClient:  util.SetProxy(&cfg.SDKConfig, &http.Client{}),
		token:       token,
		userID:      userID,
		name:        name,
		email:       email,
		machineID:   machineID,
		cliVersion:  QoderCLIVersion,
		machineOS:   QoderMachineOS,
	}
}

// UpdateCredentials updates the API credentials
func (api *QoderAPI) UpdateCredentials(token, userID, name, email string) {
	api.token = token
	api.userID = userID
	api.name = name
	api.email = email
}

// StreamChat sends a chat request and streams the response
func (api *QoderAPI) StreamChat(ctx context.Context, messages []ChatMessage, model string) (<-chan string, <-chan error) {
	resultChan := make(chan string)
	errorChan := make(chan error, 1)

	go func() {
		defer close(resultChan)
		defer close(errorChan)

		// Convert messages to prompt format
		prompt := messagesToPrompt(messages)

		// Map model name
		qoderModel := model
		if mapped, ok := ModelMap[model]; ok {
			qoderModel = mapped
		}

		// Build request body
		reqBody := map[string]interface{}{
			"question":       prompt,
			"model":          qoderModel,
			"stream":         true,
			"session_id":     uuid.New().String(),
			"request_id":     uuid.New().String(),
		}

		bodyBytes, err := json.Marshal(reqBody)
		if err != nil {
			errorChan <- fmt.Errorf("failed to marshal request: %w", err)
			return
		}

		// Build COSY auth headers
		headers, err := BuildAuthHeaders(
			bodyBytes,
			QoderChatURL,
			api.userID,
			api.token,
			api.name,
			api.email,
			api.cliVersion,
			api.machineOS,
		)
		if err != nil {
			errorChan <- fmt.Errorf("failed to build auth headers: %w", err)
			return
		}

		// Create HTTP request
		req, err := http.NewRequestWithContext(ctx, "POST", QoderChatURL, bytes.NewReader(bodyBytes))
		if err != nil {
			errorChan <- fmt.Errorf("failed to create request: %w", err)
			return
		}

		// Set headers
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", headers.Authorization)
		req.Header.Set("Cosy-Key", headers.CosyKey)
		req.Header.Set("Cosy-User", headers.CosyUser)
		req.Header.Set("Cosy-Date", headers.CosyDate)
		req.Header.Set("X-Request-Id", headers.XRequestID)
		req.Header.Set("X-Machine-OS", headers.XMachineOS)
		req.Header.Set("X-IDE-Platform", headers.XIDEPlatform)
		req.Header.Set("X-Version", headers.XVersion)
		req.Header.Set("Accept", "text/event-stream")

		// Send request
		resp, err := api.httpClient.Do(req)
		if err != nil {
			errorChan <- fmt.Errorf("request failed: %w", err)
			return
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			errorChan <- fmt.Errorf("API request failed: %d %s. Response: %s", resp.StatusCode, resp.Status, string(body))
			return
		}

		// Read SSE stream
		reader := bufio.NewReader(resp.Body)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					return
				}
				errorChan <- fmt.Errorf("failed to read stream: %w", err)
				return
			}

			line = strings.TrimSpace(line)
			if line == "" || !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				return
			}

			var response ChatResponse
			if err := json.Unmarshal([]byte(data), &response); err != nil {
				continue
			}

			if len(response.Choices) > 0 {
				content := response.Choices[0].Delta.Content
				if content != "" {
					resultChan <- content
				}
				if response.Choices[0].FinishReason != nil {
					return
				}
			}
		}
	}()

	return resultChan, errorChan
}

// ListModels returns the list of available models
func (api *QoderAPI) ListModels() []string {
	models := make([]string, 0, len(ModelMap))
	for model := range ModelMap {
		models = append(models, model)
	}
	return models
}

// messagesToPrompt converts OpenAI-style messages to Qoder prompt format
func messagesToPrompt(messages []ChatMessage) string {
	var parts []string

	for _, msg := range messages {
		content := contentToString(msg.Content)
		switch msg.Role {
		case "system":
			parts = append(parts, fmt.Sprintf("[System Instructions]\n%s", content))
		case "assistant":
			parts = append(parts, fmt.Sprintf("[Previous Assistant Response]\n%s", content))
		case "user":
			parts = append(parts, content)
		case "tool":
			parts = append(parts, fmt.Sprintf("[Tool Result]\n%s", content))
		}
	}

	return strings.Join(parts, "\n\n")
}

// contentToString converts message content to string
func contentToString(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			if itemMap, ok := item.(map[string]interface{}); ok {
				if text, ok := itemMap["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return fmt.Sprintf("%v", content)
	}
}

// doRefreshToken performs token refresh and saves to storage
func doRefreshToken(ctx context.Context, cfg *config.Config, storage *QoderTokenStorage) error {
	auth := NewQoderAuth(cfg)
	
	tokenData, err := auth.RefreshTokens(ctx, storage.Token, storage.RefreshToken)
	if err != nil {
		return fmt.Errorf("failed to refresh token: %w", err)
	}

	auth.UpdateTokenStorage(storage, tokenData)
	return storage.SaveTokenToFile("")
}

// RefreshTokenIfNeeded checks if token needs refresh and refreshes it
func RefreshTokenIfNeeded(ctx context.Context, cfg *config.Config, storage *QoderTokenStorage, bufferSeconds int64) error {
	if storage.ExpireTime == 0 {
		return nil
	}

	now := time.Now().UnixMilli()
	bufferMs := bufferSeconds * 1000
	
	if storage.ExpireTime-now-bufferMs <= 0 {
		return doRefreshToken(ctx, cfg, storage)
	}

	return nil
}
