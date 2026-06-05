package executor

import (
	"net/http"
	"testing"
	"time"
)

func TestNormalizeOpenAICompatStatus_PaymentLikeMessage(t *testing.T) {
	t.Parallel()

	tests := []string{
		"insufficient balance",
		"账户余额不足",
		"余额不足，请充值后重试",
	}

	for _, message := range tests {
		if got := normalizeOpenAICompatStatus(http.StatusBadRequest, message); got != http.StatusPaymentRequired {
			t.Fatalf("normalizeOpenAICompatStatus(%q) = %d, want %d", message, got, http.StatusPaymentRequired)
		}
	}
}

func TestNormalizeOpenAICompatStatus_QuotaLikeMessage(t *testing.T) {
	t.Parallel()

	if got := normalizeOpenAICompatStatus(http.StatusBadRequest, "insufficient_quota"); got != http.StatusTooManyRequests {
		t.Fatalf("normalizeOpenAICompatStatus(quota) = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func TestNormalizeOpenAICompatStatus_KimiBillingCycleUsageLimit(t *testing.T) {
	t.Parallel()

	message := "You've reached your usage limit for this billing cycle. Your quota will be refreshed in the next cycle. Upgrade to get more: https://www.kimi.com/code/console?from=quota-upgrade"
	if got := normalizeOpenAICompatStatus(http.StatusForbidden, message); got != http.StatusTooManyRequests {
		t.Fatalf("normalizeOpenAICompatStatus(kimi billing cycle quota) = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func TestNewOpenAICompatStatusErr_ParsesRetryAfter(t *testing.T) {
	t.Parallel()

	headers := http.Header{"Retry-After": {"12"}}
	err := newOpenAICompatStatusErr(openAICompatProfileForKind("kimi"), nil, "kimi-k2.5", http.StatusTooManyRequests, headers, "application/json", []byte(`{"error":{"message":"rate limit"}}`))

	if err.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("StatusCode() = %d, want %d", err.StatusCode(), http.StatusTooManyRequests)
	}
	retryAfter := err.RetryAfter()
	if retryAfter == nil {
		t.Fatal("RetryAfter() = nil, want non-nil")
	}
	if *retryAfter != 12*time.Second {
		t.Fatalf("RetryAfter() = %v, want %v", *retryAfter, 12*time.Second)
	}
}

func TestNewOpenAICompatStatusErr_EmptyBodyHasErrorCode(t *testing.T) {
	t.Parallel()

	err := newOpenAICompatStatusErr(openAICompatProfileForKind("codex"), nil, "gpt-5.5", http.StatusInternalServerError, nil, "application/json", nil)

	if err.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("StatusCode() = %d, want %d", err.StatusCode(), http.StatusInternalServerError)
	}
	if err.ErrorCode() != openAICompatEmptyUpstreamResponseCode {
		t.Fatalf("ErrorCode() = %q, want %q", err.ErrorCode(), openAICompatEmptyUpstreamResponseCode)
	}
	if err.Error() != "empty upstream response" {
		t.Fatalf("Error() = %q, want empty upstream response", err.Error())
	}
}

func TestNewOpenAICompatStatusErr_KimiBillingCycleUsageLimitHasRetryAfter(t *testing.T) {
	t.Parallel()

	body := []byte(`{"error":{"message":"You've reached your usage limit for this billing cycle. Your quota will be refreshed in the next cycle. Upgrade to get more: https://www.kimi.com/code/console?from=quota-upgrade"}}`)
	err := newOpenAICompatStatusErr(openAICompatProfileForKind("kimi"), nil, "kimi-k2.6", http.StatusForbidden, nil, "application/json", body)

	if err.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("StatusCode() = %d, want %d", err.StatusCode(), http.StatusTooManyRequests)
	}
	retryAfter := err.RetryAfter()
	if retryAfter == nil {
		t.Fatal("RetryAfter() = nil, want non-nil")
	}
	if *retryAfter != openAICompatAccountQuotaRetryWait {
		t.Fatalf("RetryAfter() = %v, want %v", *retryAfter, openAICompatAccountQuotaRetryWait)
	}
}
