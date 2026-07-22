package codex

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
)

func TestAgentIdentityClientRegisterAgent(t *testing.T) {
	keyMaterial := deterministicAgentKeyMaterial(t, 0x33)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/v1/agent/register" {
			t.Errorf("request = %s %s", req.Method, req.URL.Path)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("Authorization header is incorrect")
		}
		if got := req.Header.Get("X-OpenAI-Fedramp"); got != "true" {
			t.Errorf("X-OpenAI-Fedramp = %q, want true", got)
		}
		if got := req.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if value, exists := body["ttl"]; !exists || value != nil {
			t.Errorf("ttl = %#v, exists = %t; want null", value, exists)
		}
		if body["agent_public_key"] != keyMaterial.PublicKeySSH {
			t.Errorf("agent_public_key does not match generated key")
		}
		capabilities, ok := body["capabilities"].([]any)
		if !ok || len(capabilities) != 1 || capabilities[0] != AgentIdentityResponsesCapability {
			t.Errorf("capabilities = %#v", body["capabilities"])
		}
		bill, ok := body["abom"].(map[string]any)
		if !ok || bill["agent_harness_id"] != "codex-cli" || bill["running_location"] != "cli-windows" {
			t.Errorf("abom = %#v", body["abom"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"agent_runtime_id":"runtime-123"}`)
	}))
	defer server.Close()

	client := newAgentIdentityClient(server.Client(), server.URL)
	runtimeID, err := client.RegisterAgent(context.Background(), AgentRegistration{
		AccessToken:      "access-token",
		IsFedRAMPAccount: true,
		KeyMaterial:      keyMaterial,
		BillOfMaterials: AgentBillOfMaterials{
			AgentVersion:    "7.2.94",
			AgentHarnessID:  "codex-cli",
			RunningLocation: "cli-windows",
		},
	})
	if err != nil {
		t.Fatalf("RegisterAgent() error = %v", err)
	}
	if runtimeID != "runtime-123" {
		t.Fatalf("runtime ID = %q", runtimeID)
	}
}

func TestAgentIdentityClientRegisterAgentOmitsFedRAMPHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if _, exists := req.Header["X-Openai-Fedramp"]; exists {
			t.Errorf("FedRAMP header must be omitted for non-FedRAMP accounts")
		}
		_, _ = io.WriteString(w, `{"agent_runtime_id":"runtime-123"}`)
	}))
	defer server.Close()
	client := newAgentIdentityClient(server.Client(), server.URL)
	_, err := client.RegisterAgent(context.Background(), validAgentRegistration(t))
	if err != nil {
		t.Fatalf("RegisterAgent() error = %v", err)
	}
}

func TestAgentIdentityClientRegisterTaskSignsRequest(t *testing.T) {
	keyMaterial := deterministicAgentKeyMaterial(t, 0x44)
	privateKey, err := ParseAgentIdentityPrivateKey(keyMaterial.PrivateKeyPKCS8Base64)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}
	fixedTime := time.Date(2026, 7, 22, 12, 34, 56, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/v1/agent/runtime-123/task/register" {
			t.Errorf("request = %s %s", req.Method, req.URL.Path)
		}
		if got := req.Header.Get("Authorization"); got != "" {
			t.Errorf("task registration Authorization = %q, want empty", got)
		}
		var body registerTaskRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.Timestamp != "2026-07-22T12:34:56Z" {
			t.Errorf("timestamp = %q", body.Timestamp)
		}
		signature, err := base64.StdEncoding.DecodeString(body.Signature)
		if err != nil {
			t.Errorf("decode signature: %v", err)
		}
		publicKey := privateKey.Public().(ed25519.PublicKey)
		if !ed25519.Verify(publicKey, []byte("runtime-123:"+body.Timestamp), signature) {
			t.Errorf("signature did not verify")
		}
		_, _ = io.WriteString(w, `{"taskId":"task-123"}`)
	}))
	defer server.Close()
	client := newAgentIdentityClient(server.Client(), server.URL)
	client.now = func() time.Time { return fixedTime }
	taskID, err := client.RegisterTask(context.Background(), AgentIdentityKey{
		AgentRuntimeID:        "runtime-123",
		PrivateKeyPKCS8Base64: keyMaterial.PrivateKeyPKCS8Base64,
	})
	if err != nil {
		t.Fatalf("RegisterTask() error = %v", err)
	}
	if taskID != "task-123" {
		t.Fatalf("task ID = %q", taskID)
	}
}

func TestAgentIdentityClientRegisterTaskAcceptsResponseAliases(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "snake case", body: `{"task_id":"task-snake"}`},
		{name: "camel case", body: `{"taskId":"task-camel"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			client := newAgentIdentityClient(server.Client(), server.URL)
			keyMaterial := deterministicAgentKeyMaterial(t, 0x55)
			taskID, err := client.RegisterTask(context.Background(), AgentIdentityKey{
				AgentRuntimeID:        "runtime-123",
				PrivateKeyPKCS8Base64: keyMaterial.PrivateKeyPKCS8Base64,
			})
			if err != nil {
				t.Fatalf("RegisterTask() error = %v", err)
			}
			if !strings.HasPrefix(taskID, "task-") {
				t.Fatalf("task ID = %q", taskID)
			}
		})
	}
}

func TestAgentIdentityClientRegisterTaskPreservesPlainTaskID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"task_id":"  task-123\n"}`)
	}))
	defer server.Close()

	client := newAgentIdentityClient(server.Client(), server.URL)
	keyMaterial := deterministicAgentKeyMaterial(t, 0x56)
	taskID, err := client.RegisterTask(context.Background(), AgentIdentityKey{
		AgentRuntimeID:        "runtime-123",
		PrivateKeyPKCS8Base64: keyMaterial.PrivateKeyPKCS8Base64,
	})
	if err != nil {
		t.Fatalf("RegisterTask() error = %v", err)
	}
	if taskID != "  task-123\n" {
		t.Fatalf("task ID = %q", taskID)
	}
}

func TestAgentIdentityClientRegisterTaskHonorsPresentEmptyTaskID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"task_id":"","encrypted_task_id":"not-used"}`)
	}))
	defer server.Close()

	client := newAgentIdentityClient(server.Client(), server.URL)
	keyMaterial := deterministicAgentKeyMaterial(t, 0x57)
	taskID, err := client.RegisterTask(context.Background(), AgentIdentityKey{
		AgentRuntimeID:        "runtime-123",
		PrivateKeyPKCS8Base64: keyMaterial.PrivateKeyPKCS8Base64,
	})
	if err != nil {
		t.Fatalf("RegisterTask() error = %v", err)
	}
	if taskID != "" {
		t.Fatalf("task ID = %q, want present empty value", taskID)
	}
}

func TestAgentIdentityClientRegisterTaskDecryptsEncryptedResponse(t *testing.T) {
	keyMaterial := deterministicAgentKeyMaterial(t, 0x66)
	privateKey, err := ParseAgentIdentityPrivateKey(keyMaterial.PrivateKeyPKCS8Base64)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}
	curvePublicKey, _, err := curve25519KeyPair(privateKey)
	if err != nil {
		t.Fatalf("derive Curve25519 key: %v", err)
	}
	ciphertext, err := box.SealAnonymous(nil, []byte("encrypted-task-123"), &curvePublicKey, rand.Reader)
	if err != nil {
		t.Fatalf("encrypt task ID: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"encryptedTaskId":"`+base64.StdEncoding.EncodeToString(ciphertext)+`"}`)
	}))
	defer server.Close()
	client := newAgentIdentityClient(server.Client(), server.URL)
	taskID, err := client.RegisterTask(context.Background(), AgentIdentityKey{
		AgentRuntimeID:        "runtime-123",
		PrivateKeyPKCS8Base64: keyMaterial.PrivateKeyPKCS8Base64,
	})
	if err != nil {
		t.Fatalf("RegisterTask() error = %v", err)
	}
	if taskID != "encrypted-task-123" {
		t.Fatalf("task ID = %q", taskID)
	}
}

func TestAgentIdentityClientRedactsHTTPErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"echoed_secret":"access-token"}`)
	}))
	defer server.Close()
	client := newAgentIdentityClient(server.Client(), server.URL)
	_, err := client.RegisterAgent(context.Background(), validAgentRegistration(t))
	if err == nil {
		t.Fatal("RegisterAgent() unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), "access-token") || strings.Contains(err.Error(), "echoed_secret") {
		t.Fatalf("HTTP error leaked response body: %v", err)
	}
	var httpError *AgentIdentityHTTPError
	if !errors.As(err, &httpError) || httpError.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("error = %#v, want typed 429 error", err)
	}
	if !IsRetryableAgentIdentityRegistrationError(err) {
		t.Fatal("429 must be retryable")
	}
}

func TestAgentIdentityClientRegisterAgentRetriesTransientFailuresWithSameKey(t *testing.T) {
	keyMaterial := deterministicAgentKeyMaterial(t, 0x7a)
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		attempt := attempts.Add(1)
		var body registerAgentRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body.AgentPublicKey != keyMaterial.PublicKeySSH {
			t.Errorf("attempt %d used a different public key", attempt)
		}
		if attempt < maxAgentRegistrationAttempts {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"agent_runtime_id":"runtime-after-retry"}`)
	}))
	defer server.Close()
	registration := validAgentRegistration(t)
	registration.KeyMaterial = keyMaterial
	client := newAgentIdentityClient(server.Client(), server.URL)
	runtimeID, err := client.RegisterAgent(context.Background(), registration)
	if err != nil {
		t.Fatalf("RegisterAgent() error = %v", err)
	}
	if runtimeID != "runtime-after-retry" || attempts.Load() != maxAgentRegistrationAttempts {
		t.Fatalf("runtime ID = %q, attempts = %d", runtimeID, attempts.Load())
	}
}

func TestAgentIdentityClientRegisterTaskRetriesTransientFailures(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < maxAgentRegistrationAttempts {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"task_id":"task-after-retry"}`)
	}))
	defer server.Close()
	client := newAgentIdentityClient(server.Client(), server.URL)
	keyMaterial := deterministicAgentKeyMaterial(t, 0x7b)
	taskID, err := client.RegisterTask(context.Background(), AgentIdentityKey{
		AgentRuntimeID:        "runtime-123",
		PrivateKeyPKCS8Base64: keyMaterial.PrivateKeyPKCS8Base64,
	})
	if err != nil {
		t.Fatalf("RegisterTask() error = %v", err)
	}
	if taskID != "task-after-retry" || attempts.Load() != maxAgentRegistrationAttempts {
		t.Fatalf("task ID = %q, attempts = %d", taskID, attempts.Load())
	}
}

func TestAgentIdentityClientRetriesTransportTimeout(t *testing.T) {
	var attempts atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if attempts.Add(1) < maxAgentRegistrationAttempts {
			return nil, &url.Error{Op: "Post", URL: req.URL.String(), Err: context.DeadlineExceeded}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"agent_runtime_id":"runtime-after-timeout"}`)),
			Request:    req,
		}, nil
	})}
	client := newAgentIdentityClient(httpClient, "https://auth.openai.test/api/accounts")
	runtimeID, err := client.RegisterAgent(context.Background(), validAgentRegistration(t))
	if err != nil {
		t.Fatalf("RegisterAgent() error = %v", err)
	}
	if runtimeID != "runtime-after-timeout" || attempts.Load() != maxAgentRegistrationAttempts {
		t.Fatalf("runtime ID = %q, attempts = %d", runtimeID, attempts.Load())
	}
}

func TestAgentIdentityClientDoesNotRetryHardFailure(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	client := newAgentIdentityClient(server.Client(), server.URL)
	_, err := client.RegisterAgent(context.Background(), validAgentRegistration(t))
	if err == nil {
		t.Fatal("RegisterAgent() unexpectedly succeeded")
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestAgentIdentityClientStopsAfterMaximumAttempts(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := newAgentIdentityClient(server.Client(), server.URL)
	_, err := client.RegisterAgent(context.Background(), validAgentRegistration(t))
	if err == nil {
		t.Fatal("RegisterAgent() unexpectedly succeeded")
	}
	if attempts.Load() != maxAgentRegistrationAttempts {
		t.Fatalf("attempts = %d, want %d", attempts.Load(), maxAgentRegistrationAttempts)
	}
}

func TestIsRetryableAgentIdentityRegistrationError(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{status: http.StatusBadRequest, want: false},
		{status: http.StatusUnauthorized, want: false},
		{status: http.StatusTooManyRequests, want: true},
		{status: http.StatusInternalServerError, want: true},
		{status: http.StatusServiceUnavailable, want: true},
		{status: 600, want: false},
	}
	for _, test := range tests {
		err := &AgentIdentityHTTPError{operation: "test", statusCode: test.status}
		if got := IsRetryableAgentIdentityRegistrationError(err); got != test.want {
			t.Fatalf("status %d retryable = %t, want %t", test.status, got, test.want)
		}
	}
	if IsRetryableAgentIdentityRegistrationError(context.Canceled) {
		t.Fatal("canceled context must not be retryable")
	}
	if !IsRetryableAgentIdentityRegistrationError(context.DeadlineExceeded) {
		t.Fatal("deadline exceeded must be retryable")
	}
}

func TestAgentIdentityClientRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", maxAgentRegistrationResponseSize+1))
	}))
	defer server.Close()
	client := newAgentIdentityClient(server.Client(), server.URL)
	_, err := client.RegisterAgent(context.Background(), validAgentRegistration(t))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("RegisterAgent() error = %v, want size error", err)
	}
}

func TestAgentIdentityClientRejectsInvalidUTF8Response(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte{'{', '"', 't', 'a', 's', 'k', '_', 'i', 'd', '"', ':', '"', 0xff, '"', '}'})
	}))
	defer server.Close()
	client := newAgentIdentityClient(server.Client(), server.URL)
	keyMaterial := deterministicAgentKeyMaterial(t, 0x58)
	_, err := client.RegisterTask(context.Background(), AgentIdentityKey{
		AgentRuntimeID:        "runtime-123",
		PrivateKeyPKCS8Base64: keyMaterial.PrivateKeyPKCS8Base64,
	})
	if err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Fatalf("RegisterTask() error = %v, want UTF-8 error", err)
	}
}

func TestAgentIdentityClientHonorsCallerCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer server.Close()
	client := newAgentIdentityClient(server.Client(), server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := client.RegisterAgent(ctx, validAgentRegistration(t))
	close(release)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RegisterAgent() error = %v, want deadline exceeded", err)
	}
}

func validAgentRegistration(t *testing.T) AgentRegistration {
	t.Helper()
	return AgentRegistration{
		AccessToken: "access-token",
		KeyMaterial: deterministicAgentKeyMaterial(t, 0x77),
		BillOfMaterials: AgentBillOfMaterials{
			AgentVersion:    "7.2.94",
			AgentHarnessID:  "codex-cli",
			RunningLocation: "cli-windows",
		},
	}
}
