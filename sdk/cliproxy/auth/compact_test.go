package auth

import (
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestApplyCompactAttributes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		mode         string
		defaultAllow bool
		wantMode     string
		wantAllowed  string
	}{
		{"force_on under deny", "force_on", false, "force_on", "true"},
		{"force_off under allow", "force_off", true, "force_off", "false"},
		{"auto follows allow", "auto", true, "auto", "true"},
		{"auto follows deny", "auto", false, "auto", "false"},
		{"empty treated as auto under deny", "", false, "auto", "false"},
		{"unknown treated as auto under allow", "garbage", true, "auto", "true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Auth{ID: "x"}
			ApplyCompactAttributes(a, tc.mode, tc.defaultAllow)
			if got := a.Attributes["compact_mode"]; got != tc.wantMode {
				t.Fatalf("compact_mode = %q, want %q", got, tc.wantMode)
			}
			if got := a.Attributes["compact_allowed"]; got != tc.wantAllowed {
				t.Fatalf("compact_allowed = %q, want %q", got, tc.wantAllowed)
			}
		})
	}
}

func TestAuthCompactAllowed(t *testing.T) {
	t.Parallel()
	if !authCompactAllowed(&Auth{ID: "no-attrs"}) {
		t.Fatal("missing attributes should be allowed")
	}
	if !authCompactAllowed(&Auth{ID: "true", Attributes: map[string]string{"compact_allowed": "true"}}) {
		t.Fatal(`"true" should be allowed`)
	}
	if authCompactAllowed(&Auth{ID: "false", Attributes: map[string]string{"compact_allowed": "false"}}) {
		t.Fatal(`"false" should be denied`)
	}
}

func TestRequireCompactRequestAndCandidate(t *testing.T) {
	t.Parallel()
	if !requireCompactRequest(cliproxyexecutor.Options{Alt: cliproxyexecutor.ResponsesCompactAlt}) {
		t.Fatal("compact alt should require compact")
	}
	if requireCompactRequest(cliproxyexecutor.Options{Alt: ""}) {
		t.Fatal("empty alt should not require compact")
	}
	off := &Auth{ID: "off", Attributes: map[string]string{"compact_allowed": "false"}}
	if !compactCandidateAllowed(off, false) {
		t.Fatal("non-compact request must ignore the flag")
	}
	if compactCandidateAllowed(off, true) {
		t.Fatal("compact request must drop force_off candidate")
	}
}

func TestNoCompactAuthError(t *testing.T) {
	t.Parallel()
	err := noCompactAuthError()
	if err.Code != "compact_unsupported" {
		t.Fatalf("code = %q", err.Code)
	}
	if err.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", err.StatusCode())
	}
}
