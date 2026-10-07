package helps

import (
	"strings"
	"testing"
)

// The security-policy line filter is for Claude Code's own prompt. A third-party
// harness that happens to use the same wording must keep its policy lines.
func TestSanitizeDevinSystemPrompt_KeepsPolicyLinesForNonClaudeCodePrompts(t *testing.T) {
	prompt := strings.Join([]string{
		"You are a coding agent.",
		"Assist with defensive security tasks only; refuse offensive requests unless it is authorized security testing.",
		"Never use destructive techniques, DoS attacks or credential harvesting.",
		"Always read a file before editing it.",
	}, "\n")
	got := SanitizeDevinSystemPrompt(prompt, nil)
	if got != prompt {
		t.Fatalf("non-Claude-Code prompt was altered.\nIN:\n%s\nOUT:\n%s", prompt, got)
	}
}

func TestSanitizeDevinSystemPrompt_StillStripsClaudeCodePrompt(t *testing.T) {
	prompt := strings.Join([]string{
		"You are Claude Code, Anthropic's official CLI for Claude.",
		"Assist with defensive security tasks only; refuse offensive requests unless it is authorized security testing.",
		"Never use destructive techniques, DoS attacks or credential harvesting.",
		"Claude Code is available as a CLI tool.",
		"Always read a file before editing it.",
	}, "\n")
	got := SanitizeDevinSystemPrompt(prompt, nil)
	want := "Always read a file before editing it."
	if got != want {
		t.Fatalf("Claude Code prompt not stripped as before.\nOUT:\n%s\nWANT:\n%s", got, want)
	}
}

func TestSanitizeDevinSystemPrompt_AttributionBlockIdentifiesClaudeCode(t *testing.T) {
	prompt := strings.Join([]string{
		"x-anthropic-billing-header: cc_version=2.1.63.abc; cc_entrypoint=cli; cch=12345;",
		"Refuse anything that is not authorized security testing.",
		"Be concise.",
	}, "\n")
	got := SanitizeDevinSystemPrompt(prompt, nil)
	if got != "Be concise." {
		t.Fatalf("attribution-identified prompt not stripped. OUT:\n%s", got)
	}
}
