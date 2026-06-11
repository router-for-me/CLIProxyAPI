package otelplugin

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
)

// Middleware parses inbound baggage and stores it on the request context.
// Downstream handlers (or the plugin's HandleUsage via context) pick it up
// with BaggageFromContext.
func TestMiddleware_StoresParsedBaggageOnRequestContext(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	var captured Baggage
	router := gin.New()
	router.Use(Middleware())
	router.GET("/test", func(c *gin.Context) {
		captured = BaggageFromContext(c.Request.Context())
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(HeaderBaggage, "agent.id=builder,workload.kind=chat-turn")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	want := Baggage{"agent.id": "builder", "workload.kind": "chat-turn"}
	if !reflect.DeepEqual(captured, want) {
		t.Errorf("captured baggage: got %v, want %v", captured, want)
	}
}

func TestMiddleware_NoBaggageHeaderLeavesContextUntouched(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	var captured Baggage
	router := gin.New()
	router.Use(Middleware())
	router.GET("/test", func(c *gin.Context) {
		captured = BaggageFromContext(c.Request.Context())
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if captured != nil {
		t.Errorf("no header should yield nil baggage; got %v", captured)
	}
}

func TestMiddleware_MalformedHeaderLeavesContextUntouched(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	var captured Baggage
	router := gin.New()
	router.Use(Middleware())
	router.GET("/test", func(c *gin.Context) {
		captured = BaggageFromContext(c.Request.Context())
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(HeaderBaggage, "garbage,no-equals-sign")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if captured != nil {
		t.Errorf("malformed header should yield nil baggage; got %v", captured)
	}
}

// PropagateBaggage applies the configured propagation policy. We verify each
// mode produces the expected output for the same input.
func TestPropagateBaggage_OffEmitsNothing(t *testing.T) {
	prev := CurrentConfig()
	defer SetConfig(prev) //nolint:errcheck

	SetConfig(Config{Baggage: BaggageConfig{Propagation: BaggageOff}})
	in := Baggage{"agent.id": "builder"}
	if got := PropagateBaggage(in); got != "" {
		t.Errorf("off mode: got %q, want \"\"", got)
	}
}

func TestPropagateBaggage_PropagateEmitsAllKeys(t *testing.T) {
	prev := CurrentConfig()
	defer SetConfig(prev) //nolint:errcheck

	SetConfig(Config{Baggage: BaggageConfig{Propagation: BaggagePropagate}})
	in := Baggage{"agent.id": "builder", "workload.kind": "chat-turn"}
	got := PropagateBaggage(in)
	if got == "" {
		t.Fatal("propagate mode: got empty, want non-empty")
	}
	out := ParseBaggageHeader(got)
	if !reflect.DeepEqual(out, in) {
		t.Errorf("propagate round-trip: got %v, want %v", out, in)
	}
}

func TestPropagateBaggage_AllowlistFiltersKeys(t *testing.T) {
	prev := CurrentConfig()
	defer SetConfig(prev) //nolint:errcheck

	SetConfig(Config{Baggage: BaggageConfig{
		Propagation: BaggageAllowlist,
		AllowedKeys: []string{"agent.id"},
	}})
	in := Baggage{"agent.id": "builder", "secret": "shh"}
	got := PropagateBaggage(in)
	if got == "" {
		t.Fatal("allowlist mode: got empty, want non-empty")
	}
	out := ParseBaggageHeader(got)
	want := Baggage{"agent.id": "builder"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("allowlist filter: got %v, want %v", out, want)
	}
}

func TestApplyOutbound_SetsHeaderWhenPolicyEmits(t *testing.T) {
	prev := CurrentConfig()
	defer SetConfig(prev) //nolint:errcheck

	SetConfig(Config{Baggage: BaggageConfig{Propagation: BaggagePropagate}})
	req := httptest.NewRequest(http.MethodPost, "https://example.com/v1/messages", nil)
	ApplyOutbound(req, Baggage{"agent.id": "builder"})
	if req.Header.Get(HeaderBaggage) == "" {
		t.Error("expected outbound header to be set")
	}
}

func TestApplyOutbound_NoopWhenOff(t *testing.T) {
	prev := CurrentConfig()
	defer SetConfig(prev) //nolint:errcheck

	SetConfig(Config{Baggage: BaggageConfig{Propagation: BaggageOff}})
	req := httptest.NewRequest(http.MethodPost, "https://example.com/v1/messages", nil)
	ApplyOutbound(req, Baggage{"agent.id": "builder"})
	if v := req.Header.Get(HeaderBaggage); v != "" {
		t.Errorf("off mode: header should not be set; got %q", v)
	}
}

func TestApplyOutbound_NilRequestSafe(t *testing.T) {
	prev := CurrentConfig()
	defer SetConfig(prev) //nolint:errcheck

	SetConfig(Config{Baggage: BaggageConfig{Propagation: BaggagePropagate}})
	// Should not panic on a nil request.
	ApplyOutbound(nil, Baggage{"agent.id": "builder"})
}
