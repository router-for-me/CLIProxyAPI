package common

import (
	"testing"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
)

func TestCodexReplaySignatureForTarget_KeepsMuseProvenanceTagOnlyForMuseTargets(t *testing.T) {
	tagged := sigcompat.TagMetaReasoning("acct", "Q-PaDgEAAAA")
	for _, model := range []string{"muse-spark-1.3", "muse-spark-1.3-contributor(xhigh)", "muse-wonder-1.0"} {
		got, ok := CodexReplaySignatureForTarget(model, tagged)
		if !ok || got != tagged {
			t.Fatalf("%s: got (%q, %v), want the tagged value unchanged", model, got, ok)
		}
	}
	for _, model := range []string{"gpt-5.4", "grok-4.5", "claude-sonnet-4-6", "gemini-3-pro"} {
		if got, ok := CodexReplaySignatureForTarget(model, tagged); ok {
			t.Fatalf("%s: accepted tagged Muse envelope as %q", model, got)
		}
	}
}
