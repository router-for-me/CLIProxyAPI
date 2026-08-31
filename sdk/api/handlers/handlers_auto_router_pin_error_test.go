package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// TestApplyAutoRouterRouteEmptyIntersectionReturnsExplicitError pins audit
// finding B7: when a resolved auto-router tier pins providers that do not
// intersect the target model's computed providers, applyAutoRouterRoute used
// to return an empty slice, and the request then died downstream with the
// generic "provider_not_found"/503 instead of an error naming the model and
// the pinned providers. The per-key route path (handlers_routing.go) already
// produces the good diagnostic; this path must match it.
func TestApplyAutoRouterRouteEmptyIntersectionReturnsExplicitError(t *testing.T) {
	h := &BaseAPIHandler{}
	route := &autorouter.Resolved{
		Model:     "claude-opus-4-5",
		Providers: []string{"anthropic"},
	}
	computed := []string{"openai", "azure"}

	providers, errMsg := h.applyAutoRouterRoute(context.Background(), computed, route)
	if errMsg == nil {
		t.Fatalf("expected an explicit error for an empty pin intersection, got providers=%v", providers)
	}
	if errMsg.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("error status = %d, want %d", errMsg.StatusCode, http.StatusServiceUnavailable)
	}
	msg := errMsg.Error.Error()
	if !strings.Contains(msg, "claude-opus-4-5") {
		t.Errorf("error %q does not name the target model %q", msg, "claude-opus-4-5")
	}
	if !strings.Contains(msg, "anthropic") {
		t.Errorf("error %q does not name the pinned providers", msg)
	}
	if len(providers) != 0 {
		t.Errorf("expected no providers alongside the error, got %v", providers)
	}
	var _ = interfaces.ErrorMessage{}
}

// TestApplyAutoRouterRoutePartialIntersectionStillSucceeds guards the happy
// path after the signature change: a non-empty intersection returns the
// pinned subset and no error.
func TestApplyAutoRouterRoutePartialIntersectionStillSucceeds(t *testing.T) {
	h := &BaseAPIHandler{}
	route := &autorouter.Resolved{
		Model:     "claude-opus-4-5",
		Providers: []string{"anthropic"},
	}
	computed := []string{"openai", "anthropic", "azure"}

	providers, errMsg := h.applyAutoRouterRoute(context.Background(), computed, route)
	if errMsg != nil {
		t.Fatalf("unexpected error for a non-empty intersection: %v", errMsg.Error)
	}
	if len(providers) != 1 || providers[0] != "anthropic" {
		t.Fatalf("expected only the pinned provider, got %v", providers)
	}
}

// TestApplyAutoRouterRouteNoPinStillReturnsComputed guards the no-pin
// passthrough after the signature change.
func TestApplyAutoRouterRouteNoPinStillReturnsComputed(t *testing.T) {
	h := &BaseAPIHandler{}
	route := &autorouter.Resolved{Model: "claude-opus-4-5"}
	computed := []string{"openai", "azure"}

	providers, errMsg := h.applyAutoRouterRoute(context.Background(), computed, route)
	if errMsg != nil {
		t.Fatalf("unexpected error when no pin is set: %v", errMsg.Error)
	}
	if len(providers) != 2 {
		t.Fatalf("expected computed providers to pass through, got %v", providers)
	}
}

// TestApplyAutoRouterRouteNilRouteStillReturnsComputed guards the nil-route
// passthrough after the signature change.
func TestApplyAutoRouterRouteNilRouteStillReturnsComputed(t *testing.T) {
	h := &BaseAPIHandler{}
	computed := []string{"openai", "azure"}

	providers, errMsg := h.applyAutoRouterRoute(context.Background(), computed, nil)
	if errMsg != nil {
		t.Fatalf("unexpected error for a nil route: %v", errMsg.Error)
	}
	if len(providers) != 2 {
		t.Fatalf("expected computed providers to pass through for a nil route, got %v", providers)
	}
}

var _ = store.AutoRouter{}
