package executor

import (
	"net/http"
	"testing"
)

func TestCodexWebsocketBillingErrorNormalizesExplicitStatus(t *testing.T) {
	err, ok := parseCodexWebsocketErrorWithCooling([]byte(`{"type":"error","status":502,"error":{"type":"insufficient_quota","code":"credit_balance_exhausted"}}`), true)
	if !ok {
		t.Fatal("expected a WebSocket error")
	}
	billingErr := err.(statusErrWithHeaders)
	if billingErr.StatusCode() != http.StatusTooManyRequests || !billingErr.IsCredentialScoped() {
		t.Fatalf("expected credential-scoped HTTP 429 even with model-level cooling, got %v", billingErr)
	}
}
