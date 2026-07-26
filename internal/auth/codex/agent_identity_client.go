package codex

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
	"time"
	"unicode/utf8"
)

const (
	agentIdentityAuthAPIBaseURL      = "https://auth.openai.com/api/accounts"
	agentRegistrationTimeout         = 15 * time.Second
	agentTaskRegistrationTimeout     = 30 * time.Second
	maxAgentRegistrationAttempts     = 3
	maxAgentRegistrationResponseSize = 64 * 1024
)

// AgentIdentityResponsesCapability is the capability advertised by Codex clients.
const AgentIdentityResponsesCapability = "responsesapi"

// AgentBillOfMaterials identifies the client registering an Agent Identity.
type AgentBillOfMaterials struct {
	AgentVersion    string `json:"agent_version"`
	AgentHarnessID  string `json:"agent_harness_id"`
	RunningLocation string `json:"running_location"`
}

// AgentRegistration contains the credentials and metadata for initial registration.
type AgentRegistration struct {
	AccessToken      string
	IsFedRAMPAccount bool
	KeyMaterial      AgentKeyMaterial
	BillOfMaterials  AgentBillOfMaterials
}

// AgentIdentityClient registers Agent Identities and their task-scoped credentials.
type AgentIdentityClient struct {
	httpClient *http.Client
	baseURL    string
	now        func() time.Time
}

// AgentIdentityHTTPError reports an upstream status without retaining a secret-bearing body.
type AgentIdentityHTTPError struct {
	operation  string
	statusCode int
}

func (err *AgentIdentityHTTPError) Error() string {
	return fmt.Sprintf("%s failed with status %d", err.operation, err.statusCode)
}

// StatusCode returns the HTTP status received from the registration service.
func (err *AgentIdentityHTTPError) StatusCode() int {
	if err == nil {
		return 0
	}
	return err.statusCode
}

// NewAgentIdentityClient creates a client pinned to OpenAI's production registration service.
// The supplied HTTP client controls proxy and transport behavior.
func NewAgentIdentityClient(httpClient *http.Client) *AgentIdentityClient {
	return newAgentIdentityClient(httpClient, agentIdentityAuthAPIBaseURL)
}

func newAgentIdentityClient(httpClient *http.Client, baseURL string) *AgentIdentityClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &AgentIdentityClient{
		httpClient: httpClient,
		baseURL:    strings.TrimRight(baseURL, "/"),
		now:        time.Now,
	}
}

// RegisterAgent registers a generated public key and returns its durable runtime ID.
func (client *AgentIdentityClient) RegisterAgent(ctx context.Context, registration AgentRegistration) (string, error) {
	if client == nil || client.httpClient == nil {
		return "", errors.New("Agent Identity client is not initialized")
	}
	if ctx == nil {
		return "", errors.New("Agent Identity registration context is nil")
	}
	if strings.TrimSpace(registration.AccessToken) == "" {
		return "", errors.New("Agent Identity registration access token is missing")
	}
	if err := validateAgentKeyMaterial(registration.KeyMaterial); err != nil {
		return "", err
	}
	if err := validateAgentBillOfMaterials(registration.BillOfMaterials); err != nil {
		return "", err
	}

	requestBody := registerAgentRequest{
		BillOfMaterials: registration.BillOfMaterials,
		AgentPublicKey:  registration.KeyMaterial.PublicKeySSH,
		Capabilities:    []string{AgentIdentityResponsesCapability},
		TTL:             nil,
	}
	endpoint := client.baseURL + "/v1/agent/register"
	return retryAgentIdentityRegistration(ctx, func() (string, error) {
		requestContext, cancel := context.WithTimeout(ctx, agentRegistrationTimeout)
		defer cancel()
		req, err := newJSONRequest(requestContext, endpoint, requestBody)
		if err != nil {
			return "", fmt.Errorf("build Agent Identity registration request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+registration.AccessToken)
		if registration.IsFedRAMPAccount {
			req.Header.Set("X-OpenAI-Fedramp", "true")
		}

		var response registerAgentResponse
		if err := client.doRegistration(req, "Agent Identity registration", &response); err != nil {
			return "", err
		}
		runtimeID := response.AgentRuntimeID
		if runtimeID == "" {
			return "", errors.New("Agent Identity registration response omitted agent_runtime_id")
		}
		return runtimeID, nil
	})
}

// RegisterTask signs and registers a task for a durable Agent Identity key.
func (client *AgentIdentityClient) RegisterTask(ctx context.Context, key AgentIdentityKey) (string, error) {
	if client == nil || client.httpClient == nil {
		return "", errors.New("Agent Identity client is not initialized")
	}
	if ctx == nil {
		return "", errors.New("Agent Identity task registration context is nil")
	}
	runtimeID := key.AgentRuntimeID
	if runtimeID == "" {
		return "", errors.New("Agent Identity runtime ID is missing")
	}
	endpoint := client.baseURL + "/v1/agent/" + url.PathEscape(runtimeID) + "/task/register"
	return retryAgentIdentityRegistration(ctx, func() (string, error) {
		timestamp := client.now().UTC().Format(time.RFC3339)
		signature, err := signAgentTaskRegistration(key, timestamp)
		if err != nil {
			return "", err
		}
		requestBody := registerTaskRequest{Timestamp: timestamp, Signature: signature}
		requestContext, cancel := context.WithTimeout(ctx, agentTaskRegistrationTimeout)
		defer cancel()
		req, err := newJSONRequest(requestContext, endpoint, requestBody)
		if err != nil {
			return "", fmt.Errorf("build Agent Identity task registration request: %w", err)
		}

		var response registerTaskResponse
		if err := client.doRegistration(req, "Agent Identity task registration", &response); err != nil {
			return "", err
		}
		if taskID, ok := firstPresent(response.TaskID, response.TaskIDCamel); ok {
			return taskID, nil
		}
		encryptedTaskID, ok := firstPresent(response.EncryptedTaskID, response.EncryptedTaskIDCamel)
		if !ok {
			return "", errors.New("Agent Identity task registration response omitted task ID")
		}
		return decryptAgentTaskID(key, encryptedTaskID)
	})
}

// IsRetryableAgentIdentityRegistrationError reports transient upstream and transport failures.
func IsRetryableAgentIdentityRegistrationError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var httpError *AgentIdentityHTTPError
	if errors.As(err, &httpError) {
		return httpError.statusCode == http.StatusTooManyRequests ||
			(httpError.statusCode >= http.StatusInternalServerError && httpError.statusCode <= 599)
	}
	var urlError *url.Error
	return errors.As(err, &urlError)
}

func retryAgentIdentityRegistration(ctx context.Context, operation func() (string, error)) (string, error) {
	var lastErr error
	for attempt := 0; attempt < maxAgentRegistrationAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		value, err := operation()
		if err == nil {
			return value, nil
		}
		lastErr = err
		if !IsRetryableAgentIdentityRegistrationError(err) {
			break
		}
	}
	return "", lastErr
}

type registerAgentRequest struct {
	BillOfMaterials AgentBillOfMaterials `json:"abom"`
	AgentPublicKey  string               `json:"agent_public_key"`
	Capabilities    []string             `json:"capabilities"`
	TTL             *uint64              `json:"ttl"`
}

type registerAgentResponse struct {
	AgentRuntimeID string `json:"agent_runtime_id"`
}

type registerTaskRequest struct {
	Timestamp string `json:"timestamp"`
	Signature string `json:"signature"`
}

type registerTaskResponse struct {
	TaskID               *string `json:"task_id"`
	TaskIDCamel          *string `json:"taskId"`
	EncryptedTaskID      *string `json:"encrypted_task_id"`
	EncryptedTaskIDCamel *string `json:"encryptedTaskId"`
}

func (client *AgentIdentityClient) doRegistration(req *http.Request, operation string, target any) error {
	resp, err := client.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s request failed: %w", operation, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAgentRegistrationResponseSize))
		return &AgentIdentityHTTPError{operation: operation, statusCode: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAgentRegistrationResponseSize+1))
	if err != nil {
		return fmt.Errorf("read %s response: %w", operation, err)
	}
	if len(data) > maxAgentRegistrationResponseSize {
		return fmt.Errorf("%s response exceeds %d bytes", operation, maxAgentRegistrationResponseSize)
	}
	if !utf8.Valid(data) {
		return fmt.Errorf("decode %s response: response is not valid UTF-8", operation)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode %s response: %w", operation, err)
	}
	return nil
}

func newJSONRequest(ctx context.Context, endpoint string, body any) (*http.Request, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func validateAgentBillOfMaterials(materials AgentBillOfMaterials) error {
	if strings.TrimSpace(materials.AgentVersion) == "" {
		return errors.New("Agent Identity agent version is missing")
	}
	if strings.TrimSpace(materials.AgentHarnessID) == "" {
		return errors.New("Agent Identity harness ID is missing")
	}
	if strings.TrimSpace(materials.RunningLocation) == "" {
		return errors.New("Agent Identity running location is missing")
	}
	return nil
}

func firstPresent(values ...*string) (string, bool) {
	for _, value := range values {
		if value != nil {
			return *value, true
		}
	}
	return "", false
}
