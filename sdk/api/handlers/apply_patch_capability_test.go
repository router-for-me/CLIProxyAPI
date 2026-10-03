package handlers

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type patchRouteExecutor struct {
	coreauth.ProviderExecutor
	id        string
	supported bool
}

func (e *patchRouteExecutor) Identifier() string       { return e.id }
func (e *patchRouteExecutor) SupportsApplyPatch() bool { return e.supported }

func TestApplyPatchModelExactPublicRoute(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	m.RegisterExecutor(&patchRouteExecutor{id: "patch-supported", supported: true})
	m.RegisterExecutor(&patchRouteExecutor{id: "patch-unsupported"})
	r := registry.GetGlobalRegistry()
	for _, tc := range []struct {
		id, provider string
		models       []*registry.ModelInfo
	}{
		{"patch-route-supported", "patch-supported", []*registry.ModelInfo{{ID: "public-patch-alias"}, {ID: "mixed-patch-alias"}, {ID: "gpt-image-2"}, {ID: "registered-patch(high)"}}},
		{"patch-route-unsupported", "patch-unsupported", []*registry.ModelInfo{{ID: "mixed-patch-alias"}, {ID: "unsupported-patch-alias", MetadataModelID: "public-patch-alias"}}},
	} {
		r.RegisterClient(tc.id, tc.provider, tc.models)
		t.Cleanup(func() { r.UnregisterClient(tc.id) })
	}
	h := &BaseAPIHandler{AuthManager: m}
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{"public-patch-alias", true}, {"public-patch-alias(high)", true}, {"registered-patch(high)", true},
		{"mixed-patch-alias", false}, {"unsupported-patch-alias", false}, {"unknown-patch", false},
		{"gpt-image-2", false}, {"gpt-image-2(high)", false}, {"", false},
	} {
		if got := h.SupportsApplyPatchModel(tc.model); got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.model, got, tc.want)
		}
	}
	var absent *BaseAPIHandler
	if absent.SupportsApplyPatchModel("public-patch-alias") || (&BaseAPIHandler{}).SupportsApplyPatchModel("public-patch-alias") {
		t.Fatal("absent manager advertised support")
	}
}
