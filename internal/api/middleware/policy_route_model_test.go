package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// newRouteTestContext builds a gin context carrying the given model routes,
// mirroring what PolicyMiddleware stashes for an authenticated principal.
func newRouteTestContext(t *testing.T, routes []store.ModelRoute) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	if len(routes) > 0 {
		c.Set(CtxPolicyModelRoutes, routes)
	}
	return c
}

// TestRouteForModelIgnoresRequestSuffix pins audit finding B8: the request-side
// model id arrives suffix-stripped by the caller, while a route saved with a
// thinking suffix (e.g. "claude-sonnet-4-5(high)") never matched — the route was
// silently ignored and the request fell back to registry defaults. Both sides
// must be compared on the canonical (suffix-stripped) model id.
func TestRouteForModelIgnoresRequestSuffix(t *testing.T) {
	routes := []store.ModelRoute{
		{Model: "claude-sonnet-4-5", Providers: []string{"anthropic"}},
	}
	c := newRouteTestContext(t, routes)

	// A request for the suffixed variant must still match the bare route.
	if r := RouteForModel(c, "claude-sonnet-4-5(high)"); r == nil {
		t.Fatal("RouteForModel(suffixed request) = nil, want the bare-id route to match")
	}
}

// TestRouteForModelStoredSuffixMatches pins the other half of B8: a route row
// saved before normalization (with a thinking suffix in its Model id) must
// still match a suffix-stripped request. Read-time normalization keeps
// pre-existing rows working.
func TestRouteForModelStoredSuffixMatches(t *testing.T) {
	routes := []store.ModelRoute{
		{Model: "claude-sonnet-4-5(high)", Providers: []string{"anthropic"}},
	}
	c := newRouteTestContext(t, routes)

	if r := RouteForModel(c, "claude-sonnet-4-5"); r == nil {
		t.Fatal("RouteForModel(bare request) with suffixed stored route = nil, want the stored row to still match")
	}
	if r := RouteForModel(c, "claude-sonnet-4-5(high)"); r == nil {
		t.Fatal("RouteForModel(suffixed request) with suffixed stored route = nil, want a match")
	}
}

// TestRouteForModelStillRequiresExactCanonicalMatch guards against the
// normalization being loosened into a prefix match: a different model id must
// still not match.
func TestRouteForModelStillRequiresExactCanonicalMatch(t *testing.T) {
	routes := []store.ModelRoute{
		{Model: "claude-sonnet-4-5", Providers: []string{"anthropic"}},
	}
	c := newRouteTestContext(t, routes)

	if r := RouteForModel(c, "claude-sonnet-4"); r != nil {
		t.Fatalf("RouteForModel(claude-sonnet-4) = %v, want nil (canonical ids must match exactly)", r)
	}
	if r := RouteForModel(c, "gpt-5.2"); r != nil {
		t.Fatalf("RouteForModel(gpt-5.2) = %v, want nil", r)
	}
}

// TestRouteForModelEmptyAndNoRoutes guards the nil paths.
func TestRouteForModelEmptyAndNoRoutes(t *testing.T) {
	if r := RouteForModel(nil, "claude-sonnet-4-5"); r != nil {
		t.Fatalf("RouteForModel(nil ctx) = %v, want nil", r)
	}
	c := newRouteTestContext(t, nil)
	if r := RouteForModel(c, ""); r != nil {
		t.Fatalf("RouteForModel(empty model) = %v, want nil", r)
	}
	if r := RouteForModel(c, "claude-sonnet-4-5"); r != nil {
		t.Fatalf("RouteForModel(no routes stashed) = %v, want nil", r)
	}
}
