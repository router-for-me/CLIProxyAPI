package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestManagerMarkResultSynchronizesCodexQuotaHeaders(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	const authID = "codex-quota-headers"
	const model = "gpt-5"
	if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "codex", Status: StatusActive}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	exhaustedHeaders := http.Header{}
	exhaustedHeaders.Set("X-Codex-Primary-Used-Percent", "100")
	exhaustedHeaders.Set("X-Codex-Primary-Reset-At", "4102444800")
	manager.MarkResult(ctx, Result{AuthID: authID, Provider: "codex", Model: model, Success: true, Headers: exhaustedHeaders})

	exhausted, ok := manager.GetByID(authID)
	if !ok || exhausted.ModelStates[model] == nil || !exhausted.ModelStates[model].Quota.Exceeded {
		t.Fatalf("exhausted auth = %#v, %v", exhausted, ok)
	}

	recoveredHeaders := http.Header{}
	recoveredHeaders.Set("X-Codex-Primary-Used-Percent", "10")
	manager.MarkResult(ctx, Result{AuthID: authID, Provider: "codex", Model: model, Success: true, Headers: recoveredHeaders})

	recovered, ok := manager.GetByID(authID)
	if !ok || recovered.ModelStates[model] == nil {
		t.Fatalf("recovered auth = %#v, %v", recovered, ok)
	}
	if recovered.ModelStates[model].Quota.Exceeded || recovered.ModelStates[model].Unavailable {
		t.Fatalf("recovered model state = %#v", recovered.ModelStates[model])
	}
}

func TestParseCodexQuotaHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Codex-Primary-Used-Percent", "42.5")
	headers.Set("X-Codex-Primary-Window-Minutes", "300")
	headers.Set("X-Codex-Primary-Reset-At", "1700000000")
	headers.Set("X-Codex-Secondary-Used-Percent", "100")

	quota, ok := ParseCodexQuotaHeaders(headers)
	if !ok || quota.Primary == nil || quota.Secondary == nil {
		t.Fatalf("ParseCodexQuotaHeaders() = %#v, %v", quota, ok)
	}
	if quota.Primary.UsedPercent != 42.5 || quota.Primary.WindowMinutes != 300 {
		t.Fatalf("primary = %#v", quota.Primary)
	}
	if !quota.Primary.ResetAt.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("primary reset = %v", quota.Primary.ResetAt)
	}
	if exhausted := quota.exhaustedWindow(); exhausted != quota.Secondary {
		t.Fatalf("exhaustedWindow() = %#v, want secondary", exhausted)
	}
}

func TestParseCodexQuotaHeadersMissing(t *testing.T) {
	if quota, ok := ParseCodexQuotaHeaders(http.Header{}); ok {
		t.Fatalf("ParseCodexQuotaHeaders() = %#v, true", quota)
	}
}
