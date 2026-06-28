package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/notifications"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
)

func TestManagerMarkResultPublishesErrorEventAfterAuthStateUpdate(t *testing.T) {
	withEnabledErrorQueue(t)
	subscriber, unsubscribe := redisqueue.SubscribeErrors()
	defer unsubscribe()

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-error-event",
		Provider: "codex",
		Metadata: map[string]any{
			"type":  "codex",
			"email": "owner@example.com",
		},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-5",
		Success:  false,
		Error: &Error{
			Code:       "rate_limit",
			Message:    `{"error":"quota"}`,
			Retryable:  true,
			HTTPStatus: http.StatusTooManyRequests,
		},
	})

	payload := requireErrorSubscriberPayload(t, subscriber)

	var event struct {
		Type        string `json:"type"`
		Severity    string `json:"severity"`
		Provider    string `json:"provider"`
		Model       string `json:"model"`
		AuthID      string `json:"auth_id"`
		AuthIndex   string `json:"auth_index"`
		AccountType string `json:"account_type"`
		Account     string `json:"account"`
		StatusCode  int    `json:"status_code"`
		Body        string `json:"body"`
		Code        string `json:"code"`
		Retryable   bool   `json:"retryable"`
		AuthStatus  struct {
			Status        Status `json:"status"`
			StatusMessage string `json:"status_message"`
			Unavailable   bool   `json:"unavailable"`
			Quota         *struct {
				Exceeded bool   `json:"exceeded"`
				Reason   string `json:"reason"`
			} `json:"quota"`
			Model *struct {
				Name        string `json:"name"`
				Status      Status `json:"status"`
				Unavailable bool   `json:"unavailable"`
				Quota       *struct {
					Exceeded bool   `json:"exceeded"`
					Reason   string `json:"reason"`
				} `json:"quota"`
			} `json:"model"`
		} `json:"auth_status"`
	}
	if errUnmarshal := json.Unmarshal(payload, &event); errUnmarshal != nil {
		t.Fatalf("unmarshal error event: %v body=%s", errUnmarshal, string(payload))
	}
	if event.Type != notifications.EventAuthRequestFailed || event.Severity != notifications.SeverityError {
		t.Fatalf("unexpected event type fields: type=%q severity=%q", event.Type, event.Severity)
	}
	if event.Provider != "codex" || event.Model != "gpt-5" || event.AuthID != auth.ID {
		t.Fatalf("unexpected event routing fields: %+v", event)
	}
	if event.AuthIndex == "" {
		t.Fatalf("auth_index is empty in event: %s", string(payload))
	}
	if event.AccountType != "oauth" || event.Account != "owner@example.com" {
		t.Fatalf("unexpected account fields: type=%q account=%q", event.AccountType, event.Account)
	}
	if event.StatusCode != http.StatusTooManyRequests || event.Body != `{"error":"quota"}` {
		t.Fatalf("unexpected error fields: status=%d body=%q", event.StatusCode, event.Body)
	}
	if event.Code != "rate_limit" || !event.Retryable {
		t.Fatalf("unexpected error code fields: code=%q retryable=%t", event.Code, event.Retryable)
	}
	if event.AuthStatus.Status != StatusError || !event.AuthStatus.Unavailable {
		t.Fatalf("unexpected auth status: %+v", event.AuthStatus)
	}
	if event.AuthStatus.Model == nil || event.AuthStatus.Model.Name != "gpt-5" || event.AuthStatus.Model.Status != StatusError || !event.AuthStatus.Model.Unavailable {
		t.Fatalf("unexpected model status: %+v", event.AuthStatus.Model)
	}
	if event.AuthStatus.Quota == nil || !event.AuthStatus.Quota.Exceeded || event.AuthStatus.Quota.Reason != "quota" {
		t.Fatalf("unexpected auth quota: %+v", event.AuthStatus.Quota)
	}
	if event.AuthStatus.Model.Quota == nil || !event.AuthStatus.Model.Quota.Exceeded || event.AuthStatus.Model.Quota.Reason != "quota" {
		t.Fatalf("unexpected model quota: %+v", event.AuthStatus.Model.Quota)
	}
}

func TestManagerRefreshAuthUnauthorizedPublishesErrorEvent(t *testing.T) {
	withEnabledErrorQueue(t)
	subscriber, unsubscribe := redisqueue.SubscribeErrors()
	defer unsubscribe()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	auth := &Auth{
		ID:       "unauthorized-refresh-event",
		Provider: "codex",
		Metadata: map[string]any{
			"email": "owner@example.com",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.refreshAuth(context.Background(), auth.ID)
	payload := requireErrorSubscriberPayload(t, subscriber)

	var event struct {
		Type        string `json:"type"`
		Severity    string `json:"severity"`
		Provider    string `json:"provider"`
		AuthID      string `json:"auth_id"`
		AuthIndex   string `json:"auth_index"`
		AccountType string `json:"account_type"`
		Account     string `json:"account"`
		StatusCode  int    `json:"status_code"`
		Code        string `json:"code"`
		AuthStatus  struct {
			Status        Status `json:"status"`
			StatusMessage string `json:"status_message"`
			Unavailable   bool   `json:"unavailable"`
		} `json:"auth_status"`
	}
	if errUnmarshal := json.Unmarshal(payload, &event); errUnmarshal != nil {
		t.Fatalf("unmarshal refresh error event: %v body=%s", errUnmarshal, string(payload))
	}
	if event.Type != notifications.EventAuthRefreshFailed || event.Severity != notifications.SeverityError {
		t.Fatalf("unexpected event type fields: type=%q severity=%q", event.Type, event.Severity)
	}
	if event.Provider != "codex" || event.AuthID != auth.ID || event.AuthIndex == "" {
		t.Fatalf("unexpected event routing fields: %+v", event)
	}
	if event.AccountType != "oauth" || event.Account != "owner@example.com" {
		t.Fatalf("unexpected account fields: type=%q account=%q", event.AccountType, event.Account)
	}
	if event.StatusCode != http.StatusUnauthorized || event.Code != "unauthorized" {
		t.Fatalf("unexpected error fields: status=%d code=%q", event.StatusCode, event.Code)
	}
	if event.AuthStatus.Status != StatusError || event.AuthStatus.StatusMessage != "unauthorized" || !event.AuthStatus.Unavailable {
		t.Fatalf("unexpected auth status: %+v", event.AuthStatus)
	}
}

func TestManagerRefreshAuthUnauthorizedSendsConfiguredWebhook(t *testing.T) {
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	notifications.ConfigureWebhooks([]internalconfig.NotificationWebhookConfig{{
		Name:        "refresh-failed",
		URL:         server.URL,
		Adapter:     "generic",
		Events:      []string{notifications.EventAuthRefreshFailed},
		StatusCodes: []int{http.StatusUnauthorized},
	}})
	t.Cleanup(func() {
		notifications.ConfigureWebhooks(nil)
	})

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	auth := &Auth{
		ID:       "unauthorized-refresh-webhook",
		Provider: "codex",
		Metadata: map[string]any{
			"email": "owner@example.com",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.refreshAuth(context.Background(), auth.ID)

	var payload []byte
	select {
	case payload = <-requests:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for webhook request")
	}
	var event struct {
		Type       string `json:"type"`
		Provider   string `json:"provider"`
		AuthID     string `json:"auth_id"`
		Account    string `json:"account"`
		StatusCode int    `json:"status_code"`
		Code       string `json:"code"`
	}
	if errUnmarshal := json.Unmarshal(payload, &event); errUnmarshal != nil {
		t.Fatalf("unmarshal webhook event: %v body=%s", errUnmarshal, string(payload))
	}
	if event.Type != notifications.EventAuthRefreshFailed || event.Provider != "codex" || event.AuthID != auth.ID {
		t.Fatalf("unexpected webhook event routing fields: %+v", event)
	}
	if event.Account != "owner@example.com" || event.StatusCode != http.StatusUnauthorized || event.Code != "unauthorized" {
		t.Fatalf("unexpected webhook event error fields: %+v", event)
	}
}

func TestManagerMarkResultSkipsErrorEventInHomeMode(t *testing.T) {
	withEnabledErrorQueue(t)
	subscriber, unsubscribe := redisqueue.SubscribeErrors()
	defer unsubscribe()

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	auth := &Auth{
		ID:       "home-auth-error-event",
		Provider: "codex",
		Metadata: map[string]any{
			"type": "codex",
		},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-5",
		Success:  false,
		Error: &Error{
			Message:    "unauthorized",
			HTTPStatus: http.StatusUnauthorized,
		},
	})

	select {
	case got := <-subscriber:
		t.Fatalf("received home-mode error event %q, want none", string(got))
	default:
	}
}

func withEnabledErrorQueue(t *testing.T) {
	t.Helper()

	prevQueueEnabled := redisqueue.Enabled()
	redisqueue.SetEnabled(false)
	redisqueue.SetEnabled(true)

	t.Cleanup(func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
	})
}

func requireErrorSubscriberPayload(t *testing.T, subscriber <-chan []byte) []byte {
	t.Helper()

	select {
	case got, ok := <-subscriber:
		if !ok {
			t.Fatalf("error subscriber closed before receiving payload")
		}
		return got
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for error subscriber payload")
		return nil
	}
}
