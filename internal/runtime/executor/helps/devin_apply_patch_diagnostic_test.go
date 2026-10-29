package helps

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	applypatch "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/apply-patch"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
)

const devinDiagnosticPatchRequest = `{"tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"shell"}]}`

func TestDevinApplyPatchDiagnosticsClassifiesShapesWithoutContent(t *testing.T) {
	d := NewDevinApplyPatchDiagnostics([]byte(devinDiagnosticPatchRequest))
	// A bare patch split across frames, delivered in the invalid-JSON field.
	d.Observe(0, DevinToolCallDelta{Name: "apply_patch", InvalidJSONStr: "\n*** Begin", InvalidJSONErr: "invalid character '*' looking for beginning of value"})
	d.Observe(0, DevinToolCallDelta{Name: "apply_patch", InvalidJSONStr: " Patch\n*** Add File: SECRET_PATH\n+SECRET_BODY\n*** End Patch"})
	// A JSON wrapper on a second patch call.
	d.Observe(1, DevinToolCallDelta{Name: "apply_patch", Arguments: `{"input":"SECRET_BODY"}`})
	// Ordinary tools are excluded from the summary.
	d.Observe(2, DevinToolCallDelta{Name: "shell", Arguments: `{"cmd":"SECRET_CMD"}`})

	got := d.Render(nil, "legacy_invalid_json_str")
	for _, want := range []string{
		ApplyPatchUpstreamErrorMessage,
		`reason="legacy_invalid_json_str"`,
		"apply_patch_calls=2",
		"call[0]={invalid_json_str=true bytes=69 shape=raw_patch",
		`devin_parse_error="invalid_character"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic missing %q: %s", want, got)
		}
	}
	for _, secret := range []string{"SECRET_PATH", "SECRET_BODY", "SECRET_CMD", "shell"} {
		if strings.Contains(got, secret) {
			t.Fatalf("diagnostic leaked %q: %s", secret, got)
		}
	}
}

func TestDevinApplyPatchDiagnosticsUsesTranslatorReason(t *testing.T) {
	state := &common.ApplyPatchErrorState{}
	state.SetToolInputError(errors.New("apply_patch arguments must contain only one input field"))
	d := NewDevinApplyPatchDiagnostics([]byte(devinDiagnosticPatchRequest))
	d.Observe(4, DevinToolCallDelta{Arguments: `{"input":"x","extra":1}`})

	got := d.Render(state, "")
	if !strings.Contains(got, `reason="apply_patch arguments must contain only one input field"`) {
		t.Fatalf("translator reason missing: %s", got)
	}
	// An unnamed call is kept because its identity may arrive after the failure.
	if !strings.Contains(got, "apply_patch_calls=1") || !strings.Contains(got, "shape=json_object") {
		t.Fatalf("unnamed patch call missing: %s", got)
	}
	if explicit := d.Render(state, "devin_trailer_error"); !strings.Contains(explicit, `reason="devin_trailer_error"`) {
		t.Fatalf("explicit reason did not win: %s", explicit)
	}
}

func TestDevinApplyPatchDiagnosticsControlCharacterReasons(t *testing.T) {
	for c := byte(0); c < 0x20; c++ {
		t.Run(fmt.Sprintf("control_%02x", c), func(t *testing.T) {
			_, err := applypatch.UnwrapInput("{\"input\":\"secret" + string(c) + "secret\"}")
			if err == nil {
				t.Fatal("expected invalid JSON")
			}
			state := &common.ApplyPatchErrorState{}
			state.SetToolInputError(err)
			got := (*DevinApplyPatchDiagnostics)(nil).Render(state, "")
			if !strings.Contains(got, `reason="invalid control character in apply_patch input"`) || strings.Contains(got, "secret") {
				t.Fatalf("unexpected diagnostic: %s", got)
			}
		})
	}
	for _, reason := range []string{
		`decode apply_patch input value: invalid character 'x' in string`,
		`decode apply_patch input value: invalid character 'secret' in string`,
	} {
		if got := devinPatchReasonCode(reason); got != "other" {
			t.Errorf("reason %q classified as %q", reason, got)
		}
	}
}

func TestDevinApplyPatchDiagnosticsBounded(t *testing.T) {
	if got := (*DevinApplyPatchDiagnostics)(nil).Render(nil, ""); got != ApplyPatchUpstreamErrorMessage {
		t.Fatalf("nil diagnostic = %q", got)
	}
	d := NewDevinApplyPatchDiagnostics([]byte(devinDiagnosticPatchRequest))
	for i := 0; i < 10; i++ {
		d.Observe(i, DevinToolCallDelta{Name: "apply_patch", InvalidJSONStr: "x", InvalidJSONErr: strings.Repeat("e", 500)})
	}
	got := d.Render(nil, strings.Repeat("r", 500))
	if !strings.Contains(got, "apply_patch_calls=10") || !strings.Contains(got, "call[0]=") || !strings.HasSuffix(got, "}") {
		t.Fatalf("diagnostic lost its call summary: %s", got)
	}
	if !strings.Contains(got, `reason="other"`) || strings.Contains(got, "rrrr") || strings.Contains(got, "eeee") {
		t.Fatalf("diagnostic leaked an unknown error: %s", got)
	}
	if strings.Contains(got, "call[1]") {
		t.Fatalf("diagnostic described more than one call: %s", got)
	}

	// Without an invalid-JSON call, the latest call is described.
	latest := NewDevinApplyPatchDiagnostics([]byte(devinDiagnosticPatchRequest))
	latest.Observe(0, DevinToolCallDelta{Name: "apply_patch", Arguments: `{"input":"a"}`})
	latest.Observe(1, DevinToolCallDelta{Name: "apply_patch", Arguments: `{"input":`})
	if got := latest.Render(nil, ""); !strings.Contains(got, "call[1]={invalid_json_str=false bytes=9 shape=json_object}") {
		t.Fatalf("latest call not described: %s", got)
	}

	// Requests without custom apply_patch do not allocate diagnostics, and
	// diagnostics cap tracked calls at maxDevinDiagnosticToolCalls (128).
	if noPatch := NewDevinApplyPatchDiagnostics([]byte(`{"tools":[{"type":"function","name":"apply_patch"}]}`)); noPatch != nil {
		t.Fatalf("expected nil diagnostics when custom apply_patch is not requested")
	}
	capped := NewDevinApplyPatchDiagnostics([]byte(devinDiagnosticPatchRequest))
	for i := 0; i < 200; i++ {
		capped.ObserveDelta(DevinToolCallDelta{ID: string(rune('a'+(i%26))) + strings.Repeat("x", i), Name: "apply_patch", Arguments: "{}"})
	}
	if got := capped.Render(nil, ""); !strings.Contains(got, "apply_patch_calls=128") {
		t.Fatalf("expected capped apply_patch_calls=128, got: %s", got)
	}
}

func TestDevinApplyPatchDiagnosticsDoesNotLogUpstreamErrorText(t *testing.T) {
	const secret = "PRIVATE_PATH_OR_PATCH"
	d := NewDevinApplyPatchDiagnostics([]byte(devinDiagnosticPatchRequest))
	d.Observe(0, DevinToolCallDelta{
		Name:           "apply_patch",
		InvalidJSONStr: "*** Begin Patch\n*** Add File: " + secret + "\n+secret\n*** End Patch",
		InvalidJSONErr: "invalid character '" + secret + "' looking for beginning of value",
	})
	state := &common.ApplyPatchErrorState{}
	state.SetToolInputError(errors.New("decode apply_patch input key: " + secret))
	got := d.Render(state, "")
	if strings.Contains(got, secret) || strings.Contains(got, "secret") || !strings.Contains(got, `devin_parse_error="invalid_character"`) || !strings.Contains(got, `reason="other"`) {
		t.Fatalf("unsafe log diagnostic: %s", got)
	}
}

func TestRecoverDevinBareApplyPatch(t *testing.T) {
	validCases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "add file",
			input: "*** Begin Patch\n*** Add File: a.txt\n+hi\n*** End Patch",
			want:  `{"input":"*** Begin Patch\n*** Add File: a.txt\n+hi\n*** End Patch"}`,
		},
		{
			name:  "update move delete and environment id with surrounding newlines",
			input: "\n*** Begin Patch\n*** Environment ID: env-1\n*** Delete File: old.txt\n*** Update File: a.txt\n*** Move to: b.txt\n@@ -1,2 +1,2 @@\n keep\n-old\n+new\n*** End of File\n*** End Patch\n",
			want:  `{"input":"*** Begin Patch\n*** Environment ID: env-1\n*** Delete File: old.txt\n*** Update File: a.txt\n*** Move to: b.txt\n@@ -1,2 +1,2 @@\n keep\n-old\n+new\n*** End of File\n*** End Patch"}`,
		},
		{
			name:  "update file without initial hunk header",
			input: "*** Begin Patch\n*** Update File: a.txt\n-old\n+new\n*** End Patch",
			want:  `{"input":"*** Begin Patch\n*** Update File: a.txt\n-old\n+new\n*** End Patch"}`,
		},
	}
	for _, tc := range validCases {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			if !CanBeDevinBareApplyPatchPrefix(tc.input[:10]) || !CanBeDevinBareApplyPatchPrefix(tc.input) {
				t.Fatalf("expected valid bare patch prefix for %q", tc.name)
			}
			got, ok := RecoverDevinBareApplyPatch(tc.input)
			if !ok || got != tc.want {
				t.Fatalf("RecoverDevinBareApplyPatch() = (%q, %v), want (%q, true)", got, ok, tc.want)
			}
		})
	}

	invalidCases := []struct {
		name      string
		input     string
		canPrefix bool
	}{
		{
			name:      "truncated missing end patch",
			input:     "*** Begin Patch\n*** Add File: a.txt\n+hi",
			canPrefix: true,
		},
		{
			name:      "empty patch without file operations",
			input:     "*** Begin Patch\n*** End Patch",
			canPrefix: true,
		},
		{
			name:      "add file without content lines",
			input:     "*** Begin Patch\n*** Add File: a.txt\n*** End Patch",
			canPrefix: true,
		},
		{
			name:      "update file without changes or move",
			input:     "*** Begin Patch\n*** Update File: a.txt\n*** End Patch",
			canPrefix: true,
		},
		{
			name:      "update file with empty hunk only",
			input:     "*** Begin Patch\n*** Update File: a.txt\n@@\n*** End Patch",
			canPrefix: true,
		},
		{
			name:      "update file with empty trailing hunk",
			input:     "*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n@@\n*** End Patch",
			canPrefix: true,
		},
		{
			name:      "update file with move only and no hunks",
			input:     "*** Begin Patch\n*** Update File: a.txt\n*** Move to: b.txt\n*** End Patch",
			canPrefix: true,
		},
		{
			name:      "duplicate environment id",
			input:     "*** Begin Patch\n*** Environment ID: env-1\n*** Environment ID: env-2\n*** Add File: a.txt\n+hi\n*** End Patch",
			canPrefix: true,
		},
		{
			name:      "missing space after add file colon",
			input:     "*** Begin Patch\n*** Add File:a.txt\n+hi\n*** End Patch",
			canPrefix: true,
		},
		{
			name:      "missing space after update file colon",
			input:     "*** Begin Patch\n*** Update File:a.txt\n@@\n-old\n+new\n*** End Patch",
			canPrefix: true,
		},
		{
			name:      "missing space after delete file colon",
			input:     "*** Begin Patch\n*** Delete File:a.txt\n*** End Patch",
			canPrefix: true,
		},
		{
			name:      "missing space after move to colon",
			input:     "*** Begin Patch\n*** Update File: a.txt\n*** Move to:b.txt\n@@\n-old\n+new\n*** End Patch",
			canPrefix: true,
		},
		{
			name:      "missing space after environment id colon",
			input:     "*** Begin Patch\n*** Environment ID:env-1\n*** Add File: a.txt\n+hi\n*** End Patch",
			canPrefix: true,
		},
		{
			name:      "delete file with unexpected content line",
			input:     "*** Begin Patch\n*** Delete File: a.txt\n+unexpected\n*** End Patch",
			canPrefix: true,
		},
		{
			name:      "nested begin patch marker",
			input:     "*** Begin Patch\n*** Add File: a.txt\n+hi\n*** Begin Patch\n*** End Patch",
			canPrefix: true,
		},
		{
			name:      "json object in invalid_json_str",
			input:     `{"input":"*** Begin Patch\n*** Add File: a.txt\n+hi\n*** End Patch"}`,
			canPrefix: false,
		},
		{
			name:      "ordinary text",
			input:     "ls -la",
			canPrefix: false,
		},
	}
	for _, tc := range invalidCases {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			if gotPrefix := CanBeDevinBareApplyPatchPrefix(tc.input); gotPrefix != tc.canPrefix {
				t.Fatalf("CanBeDevinBareApplyPatchPrefix(%q) = %v, want %v", tc.input, gotPrefix, tc.canPrefix)
			}
			if got, ok := RecoverDevinBareApplyPatch(tc.input); ok {
				t.Fatalf("RecoverDevinBareApplyPatch(%q) unexpectedly succeeded with %q", tc.input, got)
			}
		})
	}
}
