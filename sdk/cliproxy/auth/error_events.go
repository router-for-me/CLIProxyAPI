package auth

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/notifications"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
)

type errorEvent struct {
	Timestamp   time.Time            `json:"timestamp"`
	Type        string               `json:"type,omitempty"`
	Severity    string               `json:"severity,omitempty"`
	Provider    string               `json:"provider,omitempty"`
	Model       string               `json:"model,omitempty"`
	AuthID      string               `json:"auth_id,omitempty"`
	AuthIndex   string               `json:"auth_index"`
	AccountType string               `json:"account_type,omitempty"`
	Account     string               `json:"account,omitempty"`
	StatusCode  int                  `json:"status_code"`
	Body        string               `json:"body"`
	Message     string               `json:"message,omitempty"`
	Code        string               `json:"code,omitempty"`
	Retryable   bool                 `json:"retryable,omitempty"`
	ServiceURL  string               `json:"service_url,omitempty"`
	AuthStatus  errorEventAuthStatus `json:"auth_status"`
}

type errorEventAuthStatus struct {
	Status         Status                 `json:"status"`
	StatusMessage  string                 `json:"status_message,omitempty"`
	Disabled       bool                   `json:"disabled"`
	Unavailable    bool                   `json:"unavailable"`
	NextRetryAfter *time.Time             `json:"next_retry_after,omitempty"`
	Quota          *errorEventQuotaStatus `json:"quota,omitempty"`
	Model          *errorEventModelStatus `json:"model,omitempty"`
}

type errorEventQuotaStatus struct {
	Exceeded      bool       `json:"exceeded"`
	Reason        string     `json:"reason,omitempty"`
	NextRecoverAt *time.Time `json:"next_recover_at,omitempty"`
	BackoffLevel  int        `json:"backoff_level,omitempty"`
}

type errorEventModelStatus struct {
	Name           string                 `json:"name"`
	Status         Status                 `json:"status"`
	StatusMessage  string                 `json:"status_message,omitempty"`
	Unavailable    bool                   `json:"unavailable"`
	NextRetryAfter *time.Time             `json:"next_retry_after,omitempty"`
	Quota          *errorEventQuotaStatus `json:"quota,omitempty"`
}

func (m *Manager) publishErrorEvent(result Result, authSnapshot *Auth) {
	m.publishTypedErrorEvent(errorEventTypeFromResult(result), result, authSnapshot)
}

func (m *Manager) publishTypedErrorEvent(eventType string, result Result, authSnapshot *Auth) {
	if m == nil || result.Success || authSnapshot == nil || m.HomeEnabled() {
		return
	}
	payload, ok := buildErrorEventPayload(eventType, result, authSnapshot)
	if !ok {
		return
	}
	redisqueue.EnqueueError(payload)
	notifications.PublishEventPayload(payload)
}

func buildErrorEventPayload(eventType string, result Result, authSnapshot *Auth) ([]byte, bool) {
	if authSnapshot == nil || result.Success {
		return nil, false
	}
	authSnapshot.EnsureIndex()
	accountType, account := errorEventAccountInfo(authSnapshot)
	body := errorEventBody(result.Error)
	event := errorEvent{
		Timestamp:   time.Now(),
		Type:        strings.TrimSpace(eventType),
		Severity:    notifications.SeverityError,
		Provider:    strings.TrimSpace(result.Provider),
		Model:       strings.TrimSpace(result.Model),
		AuthID:      strings.TrimSpace(result.AuthID),
		AuthIndex:   strings.TrimSpace(authSnapshot.Index),
		AccountType: accountType,
		Account:     account,
		StatusCode:  errorEventStatusCode(result.Error),
		Body:        body,
		Message:     body,
		ServiceURL:  notifications.CurrentServiceURL(),
		AuthStatus:  buildErrorEventAuthStatus(result.Model, authSnapshot),
	}
	if result.Error != nil {
		event.Code = strings.TrimSpace(result.Error.Code)
		event.Retryable = result.Error.Retryable
	}
	payload, errMarshal := json.Marshal(event)
	if errMarshal != nil {
		return nil, false
	}
	return payload, true
}

func errorEventTypeFromResult(result Result) string {
	if errorEventStatusCode(result.Error) == http.StatusUnauthorized {
		return notifications.EventAuthRequestUnauthorized
	}
	return notifications.EventAuthRequestFailed
}

func errorEventAccountInfo(authSnapshot *Auth) (string, string) {
	accountType, account := authSnapshot.AccountInfo()
	accountType = strings.TrimSpace(accountType)
	if accountType != "oauth" {
		return accountType, ""
	}
	return accountType, strings.TrimSpace(account)
}

func buildErrorEventAuthStatus(model string, authSnapshot *Auth) errorEventAuthStatus {
	status := errorEventAuthStatus{
		Status:         authSnapshot.Status,
		StatusMessage:  strings.TrimSpace(authSnapshot.StatusMessage),
		Disabled:       authSnapshot.Disabled,
		Unavailable:    authSnapshot.Unavailable,
		NextRetryAfter: timePtrIfSet(authSnapshot.NextRetryAfter),
		Quota:          errorEventQuotaStatusFrom(authSnapshot.Quota),
	}
	if modelState := errorEventModelStatusFrom(model, authSnapshot); modelState != nil {
		status.Model = modelState
	}
	return status
}

func errorEventModelStatusFrom(model string, authSnapshot *Auth) *errorEventModelStatus {
	model = strings.TrimSpace(model)
	if model == "" || authSnapshot == nil || authSnapshot.ModelStates == nil {
		return nil
	}
	state := authSnapshot.ModelStates[model]
	if state == nil {
		return nil
	}
	return &errorEventModelStatus{
		Name:           model,
		Status:         state.Status,
		StatusMessage:  strings.TrimSpace(state.StatusMessage),
		Unavailable:    state.Unavailable,
		NextRetryAfter: timePtrIfSet(state.NextRetryAfter),
		Quota:          errorEventQuotaStatusFrom(state.Quota),
	}
}

func errorEventQuotaStatusFrom(quota QuotaState) *errorEventQuotaStatus {
	if !quota.Exceeded && strings.TrimSpace(quota.Reason) == "" && quota.NextRecoverAt.IsZero() && quota.BackoffLevel == 0 {
		return nil
	}
	return &errorEventQuotaStatus{
		Exceeded:      quota.Exceeded,
		Reason:        strings.TrimSpace(quota.Reason),
		NextRecoverAt: timePtrIfSet(quota.NextRecoverAt),
		BackoffLevel:  quota.BackoffLevel,
	}
}

func errorEventStatusCode(err *Error) int {
	if err != nil && err.HTTPStatus > 0 {
		return err.HTTPStatus
	}
	return 500
}

func errorEventBody(err *Error) string {
	if err == nil {
		return "request failed"
	}
	if msg := strings.TrimSpace(err.Message); msg != "" {
		return msg
	}
	if msg := strings.TrimSpace(err.Error()); msg != "" {
		return msg
	}
	return "request failed"
}

func timePtrIfSet(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copyValue := value
	return &copyValue
}
