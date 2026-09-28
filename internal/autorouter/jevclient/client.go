// Package jevclient is a minimal HTTP client for TypeSafe's System One API
// (https://docs.typesafe.ai). It exists to answer exactly one question per
// request — which complexity tier does this prompt belong to — and it makes
// exactly one attempt: the caller owns the timeout and no retry happens on the
// request path.
package jevclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

const (
	// DefaultBaseURL is the TypeSafe API root.
	DefaultBaseURL = "https://api.typesafe.ai"
	// systemOnePath is the evaluation endpoint.
	systemOnePath = "/v1/systemone"
	// maxErrorBody bounds how much of a non-200 body is retained for logging.
	maxErrorBody = 512
)

// Question is one typed question sent to the System One model. Type is one of
// "choice", "score", "noul"; Criteria carries the option set (Choice) or the
// ordered rubric (Score).
type Question struct {
	Type         string         `json:"type"`
	Instructions string         `json:"instructions"`
	Criteria     map[string]any `json:"criteria,omitempty"`
}

// ChoiceAnswer is the answer to a Choice question. Confidence is derived by the
// API from the shape of Probabilities, not from the top probability alone.
type ChoiceAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

// Usage reports token accounting for one call.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response is a parsed /v1/systemone response.
type Response struct {
	Model   string                  `json:"model"`
	Answers map[string]ChoiceAnswer `json:"answers"`
	Usage   Usage                   `json:"usage"`
}

// StatusError is a non-200 response. Callers branch on StatusCode to decide
// whether to open the circuit breaker (401/403 mean a credential problem that
// will not fix itself at request rate). The body is truncated: it is retained
// for diagnostics only and must never carry a key back into a log line.
type StatusError struct {
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("jevclient: status %d: %s", e.StatusCode, e.Body)
}

// request is the /v1/systemone request body.
type request struct {
	State     any                 `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

// Client is an HTTP client for the System One API.
type Client struct {
	baseURL string
	// apiKey is guarded by mu: the key can be rotated at runtime (the operator
	// saves a new one in the dashboard) while requests are in flight.
	mu     sync.RWMutex
	apiKey string
	http   *http.Client
}

// New builds a Client. An empty baseURL uses DefaultBaseURL; a nil http client
// uses http.DefaultClient. The client is used as-is so the caller owns
// connection pooling.
func New(baseURL, apiKey string, hc *http.Client) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: hc}
}

// SetAPIKey rotates the credential used for subsequent calls. Safe to call
// while requests are in flight; already-built requests keep the key they were
// created with. An empty key is allowed and makes calls fail with 401 until a
// real one is set — callers that gate on key presence should check first.
func (c *Client) SetAPIKey(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.apiKey = key
	c.mu.Unlock()
}

// APIKey returns the credential currently in use.
func (c *Client) APIKey() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.apiKey
}

// Call sends one state and its question set, returning the parsed response. It
// performs exactly one attempt.
func (c *Client) Call(ctx context.Context, model string, state any, questions map[string]Question) (Response, error) {
	body, errMarshal := json.Marshal(request{State: state, Model: model, Questions: questions})
	if errMarshal != nil {
		return Response{}, fmt.Errorf("jevclient: marshal request: %w", errMarshal)
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+systemOnePath, bytes.NewReader(body))
	if errReq != nil {
		return Response{}, fmt.Errorf("jevclient: build request: %w", errReq)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey())

	resp, errDo := c.http.Do(req)
	if errDo != nil {
		return Response{}, fmt.Errorf("jevclient: call: %w", errDo)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.WithError(cerr).Debug("jevclient: close response body")
		}
	}()
	raw, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return Response{}, fmt.Errorf("jevclient: read response: %w", errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return Response{}, &StatusError{StatusCode: resp.StatusCode, Body: truncateBody(string(raw))}
	}
	var out Response
	if errUnmarshal := json.Unmarshal(raw, &out); errUnmarshal != nil {
		return Response{}, fmt.Errorf("jevclient: parse response: %w", errUnmarshal)
	}
	return out, nil
}

// truncateBody bounds a diagnostic body excerpt.
func truncateBody(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxErrorBody {
		return s
	}
	return s[:maxErrorBody] + "…"
}
