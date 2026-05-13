package langfuse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Client sends events to the Langfuse Ingestion API.
// Docs: https://api.reference.langfuse.com/#tag/ingestion
type Client struct {
	baseURL    string
	publicKey  string
	secretKey  string
	httpClient *http.Client
}

// NewClient constructs a Client. Any trailing slash in baseURL is stripped.
func NewClient(baseURL, publicKey, secretKey string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		publicKey:  publicKey,
		secretKey:  secretKey,
		httpClient: &http.Client{},
	}
}

type ingestionBatch struct {
	Batch []ingestionEvent `json:"batch"`
}

type ingestionEvent struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Body      json.RawMessage `json:"body"`
}

// GenerationBody maps to a Langfuse generation-create body.
type GenerationBody struct {
	ID            string           `json:"id"`
	TraceID       string           `json:"traceId"`
	Name          string           `json:"name"`
	StartTime     time.Time        `json:"startTime"`
	EndTime       *time.Time       `json:"endTime,omitempty"`
	Model         string           `json:"model,omitempty"`
	Input         any              `json:"input,omitempty"`
	Output        any              `json:"output,omitempty"`
	Metadata      any              `json:"metadata,omitempty"`
	Level         string           `json:"level,omitempty"` // DEFAULT | DEBUG | WARNING | ERROR
	StatusMessage string           `json:"statusMessage,omitempty"`
	Usage         *GenerationUsage `json:"usage,omitempty"`
	UsageDetails  map[string]int64 `json:"usageDetails,omitempty"`
}

// GenerationUsage holds token counts for a Langfuse generation.
type GenerationUsage struct {
	Input          int64  `json:"input,omitempty"`
	Output         int64  `json:"output,omitempty"`
	Total          int64  `json:"total,omitempty"`
	Unit           string `json:"unit,omitempty"` // TOKENS
	InputCacheRead int64  `json:"inputCacheRead,omitempty"`
}

func (c *Client) ingest(ctx context.Context, events ...ingestionEvent) error {
	if len(events) == 0 {
		return nil
	}
	body, err := json.Marshal(ingestionBatch{Batch: events})
	if err != nil {
		return fmt.Errorf("langfuse: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/public/ingestion", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("langfuse: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.publicKey, c.secretKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("langfuse: http: %w", err)
	}
	defer resp.Body.Close()
	respBody := make([]byte, 2048)
	n, _ := resp.Body.Read(respBody)
	respBody = respBody[:n]
	if resp.StatusCode >= 300 {
		return fmt.Errorf("langfuse: status %d: %s", resp.StatusCode, bytes.TrimSpace(respBody))
	}
	// Langfuse returns 207 Multi-Status when the batch is accepted but
	// individual events fail. Check for per-event errors in the response.
	if resp.StatusCode == http.StatusMultiStatus {
		var result struct {
			Errors []struct {
				ID    string `json:"id"`
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"errors"`
		}
		if jsonErr := json.Unmarshal(respBody, &result); jsonErr == nil && len(result.Errors) > 0 {
			return fmt.Errorf("langfuse: %d event(s) rejected: %s: %s",
				len(result.Errors), result.Errors[0].ID, result.Errors[0].Error.Message)
		}
	}
	return nil
}

// SendGeneration sends a single generation event.
func (c *Client) SendGeneration(ctx context.Context, gen GenerationBody) error {
	body, err := json.Marshal(gen)
	if err != nil {
		return err
	}
	return c.ingest(ctx, ingestionEvent{
		ID:        gen.ID + "-create",
		Type:      "generation-create",
		Timestamp: time.Now(),
		Body:      body,
	})
}
