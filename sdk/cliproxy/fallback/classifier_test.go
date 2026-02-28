package fallback

import (
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

type statusError struct {
	msg  string
	code int
}

func (e *statusError) Error() string   { return e.msg }
func (e *statusError) StatusCode() int { return e.code }

// classifierConfig returns a sanitized config with fallback enabled and optional network error flag.
func classifierConfig(allowNetwork bool) *config.Config {
	cfg := &config.Config{
		ModelFallback: config.ModelFallback{
			Enabled:           true,
			AllowNetworkError: allowNetwork,
		},
	}
	cfg.SanitizeModelFallback()
	return cfg
}

func TestClassify_NilError(t *testing.T) {
	cfg := classifierConfig(false)
	d := Classify(nil, 0, cfg)
	if d.ShouldFallback {
		t.Fatal("nil error should not trigger fallback")
	}
}

func TestClassify_429Triggers(t *testing.T) {
	cfg := classifierConfig(false)
	d := Classify(&statusError{"rate limited", 429}, 0, cfg)
	if !d.ShouldFallback {
		t.Fatal("429 should trigger fallback")
	}
	if d.StatusCode != 429 {
		t.Errorf("StatusCode = %d, want 429", d.StatusCode)
	}
}

func TestClassify_400NoFallback(t *testing.T) {
	cfg := classifierConfig(false)
	d := Classify(&statusError{"bad request", 400}, 0, cfg)
	if d.ShouldFallback {
		t.Fatal("400 should NOT trigger fallback")
	}
}

func TestClassify_401NoFallback(t *testing.T) {
	cfg := classifierConfig(false)
	d := Classify(&statusError{"unauthorized", 401}, 0, cfg)
	if d.ShouldFallback {
		t.Fatal("401 should NOT trigger fallback")
	}
}

func TestClassify_404Triggers(t *testing.T) {
	cfg := classifierConfig(false)
	d := Classify(&statusError{"not found", 404}, 0, cfg)
	if !d.ShouldFallback {
		t.Fatal("404 should trigger fallback")
	}
}

func TestClassify_500Triggers(t *testing.T) {
	cfg := classifierConfig(false)
	d := Classify(&statusError{"internal server error", 500}, 0, cfg)
	if !d.ShouldFallback {
		t.Fatal("500 should trigger fallback")
	}
}

func TestClassify_ModelNotFoundAlwaysTriggers(t *testing.T) {
	cfg := classifierConfig(false)
	// model_not_found takes precedence over no-fallback status codes
	d := Classify(&statusError{"model_not_found: gpt-5", 422}, 0, cfg)
	if !d.ShouldFallback {
		t.Fatal("model_not_found error should always trigger fallback")
	}
	if d.Reason != "model_not_found" {
		t.Errorf("Reason = %q, want model_not_found", d.Reason)
	}
}

func TestClassify_ModelNotFoundBypassesNoFallbackCode(t *testing.T) {
	cfg := classifierConfig(false)
	// Even 400 (in no-fallback list) should trigger fallback when model_not_found is in the message
	d := Classify(&statusError{"model_not_found: unknown-model", 400}, 0, cfg)
	if !d.ShouldFallback {
		t.Fatal("model_not_found with status 400 should still trigger fallback")
	}
	if d.Reason != "model_not_found" {
		t.Errorf("Reason = %q, want model_not_found", d.Reason)
	}
}

func TestClassify_NetworkError_Allowed(t *testing.T) {
	cfg := classifierConfig(true)
	d := Classify(errors.New("connection refused"), 0, cfg)
	if !d.ShouldFallback {
		t.Fatal("network error with AllowNetworkError=true should trigger fallback")
	}
	if d.Reason != "network error" {
		t.Errorf("Reason = %q, want 'network error'", d.Reason)
	}
}

func TestClassify_NetworkError_NotAllowed(t *testing.T) {
	cfg := classifierConfig(false)
	d := Classify(errors.New("connection refused"), 0, cfg)
	if d.ShouldFallback {
		t.Fatal("network error with AllowNetworkError=false should NOT trigger fallback")
	}
}

func TestClassify_Disabled(t *testing.T) {
	cfg := &config.Config{ModelFallback: config.ModelFallback{Enabled: false}}
	d := Classify(&statusError{"server error", 500}, 0, cfg)
	if d.ShouldFallback {
		t.Fatal("disabled fallback should not trigger")
	}
}
