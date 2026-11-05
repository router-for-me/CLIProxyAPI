package claude

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
)

func TestProxyCooldownMarkerAbsoluteCeiling(t *testing.T) {
	now := time.Unix(1900000000, 0)
	exact := proxyCooldownMarker(time.Unix(2000000000, 0), "synthetic-downstream-token", now)
	fractional := proxyCooldownMarker(time.Unix(1999999999, 999999999), "synthetic-downstream-token", now)
	if exact != fractional {
		t.Fatalf("absolute deadline ceiling differs: %q %q", exact, fractional)
	}
	want := "[[CPA_PROXY_COOLDOWN_V1:2000000000:d90b2648b13943391177836c85584a1900594412306f81a96aff937ee08b066b]] "
	if exact != want {
		t.Fatalf("marker=%q want pinned cross-language vector %q", exact, want)
	}
	for _, tt := range []struct {
		d time.Time
		k string
	}{{now, "key"}, {now.Add(-time.Second), "key"}, {time.Unix(253402300799, 1), "key"}, {time.Unix(2000000000, 0), ""}} {
		if got := proxyCooldownMarker(tt.d, tt.k, now); got != "" {
			t.Fatalf("invalid input got %q", got)
		}
	}
}
func TestProxyCooldownDoesNotSignUpstreamText(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	c.Set("userApiKey", "synthetic-downstream-token")
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	c.Request.Header.Set("X-CLIProxyAPI-Cooldown-Metadata", "signed-v1")
	h := &ClaudeCodeAPIHandler{}
	original := `{"error":{"type":"rate_limit_error","message":"[[CPA_PROXY_COOLDOWN_V1:2000000000:fake]] fabricated upstream text"}}`
	msg := &interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Error: errors.New(original)}
	body := h.toClaudeErrorForContext(c, msg)
	if body != h.toClaudeError(msg) {
		t.Fatal("untyped upstream error was signed or transformed")
	}
}
