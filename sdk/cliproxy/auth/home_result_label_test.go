package auth

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// Context and auth metadata keys used by the Home unauthorized reporting path.
var (
	homeResultLabelTokenKey = "access_token"
	homeResultRawKeyName    = "userApiKey"
	homeResultLabelKeyName  = "userApiKeyLabel"
)

// homeResultLabelGinContext mimics the subset of *gin.Context read by the auth package.
type homeResultLabelGinContext struct {
	values map[string]any
}

func (c *homeResultLabelGinContext) Get(key string) (any, bool) {
	if c == nil {
		return nil, false
	}
	value, ok := c.values[key]
	return value, ok
}

type homeResultLabelCapture struct {
	authID  string
	records chan coreusage.Record
}

func (p *homeResultLabelCapture) HandleUsage(_ context.Context, record coreusage.Record) {
	if p == nil || record.ExecutorType != homeResultExecutorType || record.AuthID != p.authID {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

func (p *homeResultLabelCapture) wait(t *testing.T) coreusage.Record {
	t.Helper()
	select {
	case record := <-p.records:
		return record
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Home unauthorized usage record")
		return coreusage.Record{}
	}
}

type homeResultLabelNoopPlugin struct{}

func (homeResultLabelNoopPlugin) HandleUsage(context.Context, coreusage.Record) {}

func registerHomeResultLabelCapture(t *testing.T, name, authID string) *homeResultLabelCapture {
	t.Helper()
	capture := &homeResultLabelCapture{authID: authID, records: make(chan coreusage.Record, 1)}
	coreusage.RegisterNamedPlugin(name, capture)
	t.Cleanup(func() {
		coreusage.RegisterNamedPlugin(name, homeResultLabelNoopPlugin{})
	})
	return capture
}

func newHomeResultLabelAuth(id string) *Auth {
	return &Auth{
		ID:       id,
		Provider: "home-result-label",
		Status:   StatusActive,
		Metadata: map[string]any{homeResultLabelTokenKey: "home-result-label-token"},
	}
}

func newHomeResultLabelContext(values map[string]any) context.Context {
	ginCtx := &homeResultLabelGinContext{values: values}
	//nolint:staticcheck // the runtime stores the gin context under this plain string key
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func TestReportHomeUnauthorizedUsesAPIKeyLabel(t *testing.T) {
	authID := "home-result-label-auth"
	capture := registerHomeResultLabelCapture(t, "home-result-label-capture", authID)

	ctx := newHomeResultLabelContext(map[string]any{
		homeResultRawKeyName:   "raw-client-key",
		homeResultLabelKeyName: "alice",
	})

	(&Manager{}).ReportHomeUnauthorized(ctx, newHomeResultLabelAuth(authID), "home-result-label", "test-model")

	if record := capture.wait(t); record.APIKey != "alice" {
		t.Fatalf("record.APIKey = %q, want %q", record.APIKey, "alice")
	}
}

func TestReportHomeUnauthorizedWithoutLabelOmitsRawAPIKey(t *testing.T) {
	authID := "home-result-no-label-auth"
	capture := registerHomeResultLabelCapture(t, "home-result-no-label-capture", authID)

	ctx := newHomeResultLabelContext(map[string]any{
		homeResultRawKeyName: "raw-client-key",
	})

	(&Manager{}).ReportHomeUnauthorized(ctx, newHomeResultLabelAuth(authID), "home-result-label", "test-model")

	if record := capture.wait(t); record.APIKey != "" {
		t.Fatalf("record.APIKey = %q, want empty (raw keys must never be recorded)", record.APIKey)
	}
}

func TestReportHomeUnauthorizedWithoutGinContext(t *testing.T) {
	authID := "home-result-no-gin-auth"
	capture := registerHomeResultLabelCapture(t, "home-result-no-gin-capture", authID)

	(&Manager{}).ReportHomeUnauthorized(context.Background(), newHomeResultLabelAuth(authID), "home-result-label", "test-model")

	if record := capture.wait(t); record.APIKey != "" {
		t.Fatalf("record.APIKey = %q, want empty", record.APIKey)
	}
}
