package helps

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

func TestRecordAPIResponseMetadataStoresHeadersWhenRequestLogDisabled(t *testing.T) {
	ctx := logging.WithResponseHeadersHolder(context.Background())
	headers := http.Header{}
	headers.Add("X-Upstream-Request-Id", "upstream-req-1")

	RecordAPIResponseMetadata(ctx, &config.Config{}, http.StatusOK, headers)
	headers.Set("X-Upstream-Request-Id", "mutated")

	got := logging.GetResponseHeaders(ctx)
	if got.Get("X-Upstream-Request-Id") != "upstream-req-1" {
		t.Fatalf("response header = %q, want %q", got.Get("X-Upstream-Request-Id"), "upstream-req-1")
	}
}

func TestFormatAuthInfoIncludesAccount(t *testing.T) {
	out := formatAuthInfo(UpstreamRequestLog{
		Provider:  "deepseek",
		AuthID:    "openai-compatibility:deepseek:123",
		AuthLabel: "deepseek",
		AuthType:  "api_key",
		AuthValue: "sk-1234567890abcdef",
	})

	if !strings.Contains(out, "account=sk-1...cdef") {
		t.Fatalf("formatAuthInfo() = %q, want masked account", out)
	}
}

func TestFormatAuthInfoPrefersEmailLabelForAccount(t *testing.T) {
	out := formatAuthInfo(UpstreamRequestLog{
		Provider:  "deepseek",
		AuthID:    "openai-compatibility:deepseek:123",
		AuthLabel: "account@example.com",
		AuthType:  "api_key",
		AuthValue: "sk-1234567890abcdef",
	})

	if !strings.Contains(out, "account=account@example.com") {
		t.Fatalf("formatAuthInfo() = %q, want email account", out)
	}
}
