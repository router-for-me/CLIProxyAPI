package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// TestValidateModelRoutesForNormalizesStoredModelIDs pins audit finding B8:
// routes saved with a thinking suffix in the model id (e.g. "model(high)") never
// matched at enforcement time because requests arrive suffix-stripped. The
// write-time validator must canonicalize the stored id (suffix stripped, so
// the persisted row is clean), and report what changed so operators see the
// stored form.
func TestValidateModelRoutesForNormalizesStoredModelIDs(t *testing.T) {
	routes := []store.ModelRoute{
		{Model: "claude-sonnet-4-5(high)", Providers: []string{"anthropic"}},
	}
	allowed := []string{"claude-sonnet-4-5"}

	msg := validateModelRoutesFor(allowed, routes)
	if msg != "" {
		t.Fatalf("validateModelRoutesFor() = %q, want empty (suffixed stored id must validate)", msg)
	}
	if routes[0].Model != "claude-sonnet-4-5" {
		t.Fatalf("stored route model = %q, want %q (write-time normalization must canonicalize the id)", routes[0].Model, "claude-sonnet-4-5")
	}

	// A suffixed duplicate of a bare route must be rejected as a duplicate
	// once both normalize to the same canonical id.
	dupRoutes := []store.ModelRoute{
		{Model: "claude-sonnet-4-5", Providers: []string{"anthropic"}},
		{Model: "claude-sonnet-4-5(high)", Providers: []string{"anthropic"}},
	}
	if msg := validateModelRoutesFor(allowed, dupRoutes); msg == "" {
		t.Fatal("validateModelRoutesFor() with a bare + suffixed duplicate = empty, want a duplicate error")
	}
}

// TestValidateModelRoutesForAllowedModelsMatchesCanonicalizedID guards the
// allowed_models check: a route saved with a suffix against an allowed list of
// the bare id must validate (the comparison happens on the canonical id).
func TestValidateModelRoutesForAllowedModelsMatchesCanonicalizedID(t *testing.T) {
	routes := []store.ModelRoute{
		{Model: "claude-sonnet-4-5(high)", Providers: []string{"anthropic"}},
	}
	allowed := []string{"claude-sonnet-4-5"}

	if msg := validateModelRoutesFor(allowed, routes); msg != "" {
		t.Fatalf("validateModelRoutesFor() = %q, want empty (canonical id must be covered by allowed_models)", msg)
	}
	if routes[0].Model != "claude-sonnet-4-5" {
		t.Fatalf("stored route model = %q, want claude-sonnet-4-5", routes[0].Model)
	}
}
