package helps

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	responsestools "github.com/router-for-me/CLIProxyAPI/v8/internal/responsestools"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const pollutedSearchCallBody = `{"input":[{"type":"tool_search_call","id":"fc_call_x","call_id":"c1","arguments":{}}]}`

// The emergency gate is the only thing this helper adds: with the bridge off,
// the body must reach the wire exactly as the client sent it.
func TestRepairResponsesToolItemIDsEmergencyGate(t *testing.T) {
	disabled := false
	enabled := true
	for _, testCase := range []struct {
		name   string
		cfg    *config.Config
		wantID string
	}{
		{"nil config uses the default", nil, "tsc_call_x"},
		{"explicitly enabled", &config.Config{ResponsesTools: config.ResponsesToolsConfig{Enabled: &enabled}}, "tsc_call_x"},
		{"explicitly disabled", &config.Config{ResponsesTools: config.ResponsesToolsConfig{Enabled: &disabled}}, "fc_call_x"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			out, err := RepairResponsesToolItemIDs(testCase.cfg, []byte(pollutedSearchCallBody))
			if err != nil {
				t.Fatalf("repair: %v", err)
			}
			if testCase.wantID == "fc_call_x" {
				// A passthrough must return the caller's own bytes.
				if string(out) != pollutedSearchCallBody {
					t.Fatalf("disabled gate rewrote the body: %s", out)
				}
				return
			}
			if !strings.Contains(string(out), `"id":"`+testCase.wantID+`"`) {
				t.Fatalf("body = %s, want the migrated id", out)
			}
		})
	}

	// The helper does not weaken the core contract: an unresolvable history is
	// still refused before anything is sent.
	opaque := []byte(`{"previous_response_id":"resp_1","input":[{"type":"tool_search_call","id":"fc_call_x","call_id":"c1","arguments":{}}]}`)
	if _, err := RepairResponsesToolItemIDs(nil, opaque); err == nil {
		t.Fatal("expected the opaque history refusal")
	} else if !responsestools.IsRequestScopedError(err) {
		t.Fatalf("refusal must stay request scoped: %v", err)
	}

	// A body with nothing to repair is returned untouched.
	healthy := []byte(`{"input":[{"type":"tool_search_call","id":"tsc_call_x","call_id":"c1","arguments":{}}]}`)
	out, err := RepairResponsesToolItemIDs(nil, healthy)
	if err != nil || string(out) != string(healthy) {
		t.Fatalf("healthy body changed: %s, %v", out, err)
	}
}

func TestIsResponsesFamilyFormat(t *testing.T) {
	if !IsResponsesFamilyFormat(sdktranslator.FormatCodex) ||
		!IsResponsesFamilyFormat(sdktranslator.FormatOpenAIResponse) {
		t.Fatal("Responses formats were not recognized")
	}
	if IsResponsesFamilyFormat(sdktranslator.FromString("openai")) ||
		IsResponsesFamilyFormat(sdktranslator.FromString("claude")) {
		t.Fatal("a non-Responses format was accepted")
	}
}
