package payloadcompress

import (
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestMcpMaxCharsFallsBackToDefault pins the upstream floor: a budget below
// twice the tail reserve would leave no head and must fall back to the default.
func TestMcpMaxCharsFallsBackToDefault(t *testing.T) {
	opts := sanitizeStepOptions(StepOptions{McpMaxChars: 100})
	if opts.McpMaxChars != 0 {
		t.Fatalf("mcp-max-chars = %d, want 0 (default sentinel)", opts.McpMaxChars)
	}
}

// TestMcpExplicitZeroKeep checks an explicit 0 keep knob survives config parsing
// instead of being mistaken for an omitted field.
func TestMcpExplicitZeroKeep(t *testing.T) {
	const raw = "engine: mcp-accessibility\noptions:\n  mcp-collapse-keep-head: 0\n  mcp-collapse-keep-tail: 0\n"
	var step Step
	if err := yaml.Unmarshal([]byte(raw), &step); err != nil {
		t.Fatal(err)
	}
	if !step.Options.mcpKeepHeadSet || !step.Options.mcpKeepTailSet {
		t.Fatalf("explicit zero keys not recorded: %+v", step.Options)
	}
	opts := sanitizeStepOptions(step.Options)
	if got := mcpKeepValue(opts.McpCollapseKeepHead, opts.mcpKeepHeadSet, mcpDefaultCollapseKeepHead); got != 0 {
		t.Fatalf("keep-head = %d, want explicit 0", got)
	}
	if got := mcpKeepValue(opts.McpCollapseKeepTail, opts.mcpKeepTailSet, mcpDefaultCollapseKeepTail); got != 0 {
		t.Fatalf("keep-tail = %d, want explicit 0", got)
	}
}

// TestMcpCollapseShortRunDoesNotPanic covers a run long enough to reach the
// collapse threshold but with fewer siblings than keepHead+keepTail.
func TestMcpCollapseShortRunDoesNotPanic(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 7; i++ {
		b.WriteString(`  - button "b` + strconv.Itoa(i) + `" [ref=e` + strconv.Itoa(i) + "]\n")
		for k := 0; k < 4; k++ {
			b.WriteString(`    - text "line"` + "\n")
		}
	}
	eng := mcpAccessibilityEngine{opts: StepOptions{McpMinLength: 1}}
	if _, ok := eng.compress(b.String()); !ok {
		// No collapse is correct here: nothing may be omitted.
		return
	}
}
