package executor

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// The real tool that triggered the incident: a deferred MCP tool the model
// loaded through ToolSearch mid-conversation and then referred to by a hybrid
// name that mixed the request's virtual MCP server with the real one.
const (
	failOpenRealMCPTool   = "mcp__inventory__inventory_lookup_by_id"
	failOpenRealMCPServer = "inventory"
	failOpenRealToolPart  = "inventory_lookup_by_id"
)

// failOpenVirtualServer returns the two-word virtual MCP server component that
// remapOAuthToolNamesWithOptions derives for this caller secret. Tests build
// drifted names against the real derivation rather than a hand-written string so
// they keep testing the shipped alias format.
func failOpenVirtualServer(t *testing.T, secret string) string {
	t.Helper()
	parts := strings.SplitN(helps.ClaudeMCPToolAlias(secret, "probe", 0), "__", 3)
	if len(parts) != 3 || parts[1] == "" {
		t.Fatalf("cannot derive virtual server from alias %q", helps.ClaudeMCPToolAlias(secret, "probe", 0))
	}
	return parts[1]
}

// failOpenRequest drives the real request path: one ordinary client tool (which
// forces alias allocation, without which passthrough tools are not recorded at
// all) plus the caller-owned MCP tools named by mcpTools. It returns the derived
// virtual server, the alias allocated for the ordinary tool, and the per-request
// reverse map that the response path consumes.
func failOpenRequest(t *testing.T, secret string, mcpTools ...string) (string, string, map[string]string) {
	t.Helper()
	declarations := []string{`{"name":"Read","input_schema":{"type":"object"}}`}
	for _, name := range mcpTools {
		declarations = append(declarations, fmt.Sprintf(`{"name":%q}`, name))
	}
	body := []byte(fmt.Sprintf(`{"tools":[%s]}`, strings.Join(declarations, ",")))

	remapped, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: secret})

	readAlias := gjson.GetBytes(remapped, "tools.0.name").String()
	if readAlias == "Read" || !helps.IsClaudeMCPToolName(readAlias) {
		t.Fatalf("ordinary tool was not aliased: %q", readAlias)
	}
	for index, name := range mcpTools {
		if got := gjson.GetBytes(remapped, fmt.Sprintf("tools.%d.name", index+1)).String(); got != name {
			t.Fatalf("caller MCP tool sent upstream as %q, want %q unchanged", got, name)
		}
		if original, ok := reverseMap[name]; !ok || original != name {
			t.Fatalf("caller MCP tool %q was not recorded as an identity entry (got %q, ok=%v)", name, original, ok)
		}
	}
	return failOpenVirtualServer(t, secret), readAlias, reverseMap
}

func failOpenRestoreToolUse(t *testing.T, name string, reverseMap map[string]string) string {
	t.Helper()
	response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, name))
	restored, err := restoreClaudeOAuthToolNamesFromResponse(response, reverseMap)
	if err != nil {
		t.Fatalf("restoreClaudeOAuthToolNamesFromResponse(%q) error = %v, want fail-open nil", name, err)
	}
	return gjson.GetBytes(restored, "content.0.name").String()
}

// Shape (a) of the incident: the model prefixed the complete caller-owned MCP
// name with the request's virtual server, dropping only the leading "mcp__".
// Recovery must strip the virtual prefix and restore the real declared name.
func TestClaudeMCPAliasFailOpenRecoversVirtualServerPrefixedRealName(t *testing.T) {
	server, _, reverseMap := failOpenRequest(t, "failopen-shape-a-caller", failOpenRealMCPTool)

	hybrid := "mcp__" + server + "__" + failOpenRealMCPServer + "__" + failOpenRealToolPart
	if got := failOpenRestoreToolUse(t, hybrid, reverseMap); got != failOpenRealMCPTool {
		t.Fatalf("hybrid name %q restored as %q, want %q", hybrid, got, failOpenRealMCPTool)
	}
}

// Shape (b) of the incident: the model kept the virtual server but dropped the
// caller's real server segment entirely, leaving only the tool component. The
// reverse-map entry for a passthrough tool is an identity entry, so this must
// still report a change: the name on the wire is not the declared name.
func TestClaudeMCPAliasFailOpenRecoversDroppedServerSegment(t *testing.T) {
	server, _, reverseMap := failOpenRequest(t, "failopen-shape-b-caller", failOpenRealMCPTool)

	hybrid := "mcp__" + server + "__" + failOpenRealToolPart
	if got := failOpenRestoreToolUse(t, hybrid, reverseMap); got != failOpenRealMCPTool {
		t.Fatalf("hybrid name %q restored as %q, want %q", hybrid, got, failOpenRealMCPTool)
	}
}

// Two caller MCP servers exposing the same tool component is a genuine
// ambiguity. Recovering shape (b) there would pick a server at random and call
// the wrong one, so the name must be forwarded untouched instead -- and still
// without an error.
func TestClaudeMCPAliasFailOpenDoesNotGuessAmbiguousPassthroughTool(t *testing.T) {
	const sharedTool = "lookup_by_ip"
	server, _, reverseMap := failOpenRequest(t, "failopen-ambiguous-caller",
		"mcp__inventory__"+sharedTool, "mcp__catalog__"+sharedTool)

	hybrid := "mcp__" + server + "__" + sharedTool
	if got := failOpenRestoreToolUse(t, hybrid, reverseMap); got != hybrid {
		t.Fatalf("ambiguous tool component restored as %q, want %q forwarded unchanged", got, hybrid)
	}
}

// An alias-shaped name under the known virtual server that maps to nothing at
// all must fail OPEN: no restore, and critically no error, because on the
// streaming path an error here destroys the whole response.
func TestClaudeMCPAliasFailOpenOnUnmappableAliasShapedName(t *testing.T) {
	server, _, reverseMap := failOpenRequest(t, "failopen-unmappable-caller", failOpenRealMCPTool)

	unmappable := "mcp__" + server + "__abandon_no_such_tool_at_all"
	if got := failOpenRestoreToolUse(t, unmappable, reverseMap); got != unmappable {
		t.Fatalf("unmappable name restored as %q, want %q forwarded unchanged", got, unmappable)
	}
}

// A name belonging to some other MCP server entirely was never ours to restore
// and must not be touched by any of the new recovery paths.
func TestClaudeMCPAliasFailOpenLeavesUnknownServerNameUntouched(t *testing.T) {
	_, _, reverseMap := failOpenRequest(t, "failopen-unknown-server-caller", failOpenRealMCPTool)

	const foreign = "mcp__external__query"
	if got := failOpenRestoreToolUse(t, foreign, reverseMap); got != foreign {
		t.Fatalf("foreign MCP name restored as %q, want %q unchanged", got, foreign)
	}
}

// The passthrough tool quoted exactly as declared keeps taking the identity
// path, and the ordinary aliased tool keeps restoring through both the exact map
// and the pre-existing repeated-server drift fallback. The recovery paths must
// not have displaced either.
func TestClaudeMCPAliasFailOpenPreservesExistingRestorePaths(t *testing.T) {
	server, readAlias, reverseMap := failOpenRequest(t, "failopen-existing-paths-caller", failOpenRealMCPTool)

	if got := failOpenRestoreToolUse(t, failOpenRealMCPTool, reverseMap); got != failOpenRealMCPTool {
		t.Fatalf("declared MCP tool restored as %q, want %q unchanged", got, failOpenRealMCPTool)
	}
	if got := failOpenRestoreToolUse(t, readAlias, reverseMap); got != "Read" {
		t.Fatalf("generated alias restored as %q, want Read", got)
	}

	aliasToolPart := strings.SplitN(readAlias, "__", 3)[2]
	repeatedServer := "mcp__" + server + "__" + server + "__" + aliasToolPart
	if got := failOpenRestoreToolUse(t, repeatedServer, reverseMap); got != "Read" {
		t.Fatalf("repeated-server alias %q restored as %q, want Read", repeatedServer, got)
	}
}

// The whole point of the change: the streaming path. content_block_start is
// emitted after message_start has already been flushed, so an error here closes
// the SSE stream unretryably. Both hybrid shapes must be repaired in place, and
// nothing may error.
func TestClaudeMCPAliasFailOpenStreamLineRecoversHybridNames(t *testing.T) {
	server, _, reverseMap := failOpenRequest(t, "failopen-stream-caller", failOpenRealMCPTool)

	shapes := map[string]string{
		"virtual server prefixed real name": "mcp__" + server + "__" + failOpenRealMCPServer + "__" + failOpenRealToolPart,
		"dropped server segment":            "mcp__" + server + "__" + failOpenRealToolPart,
	}
	for label, name := range shapes {
		t.Run(label, func(t *testing.T) {
			line := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}`, name))
			restored, err := restoreClaudeOAuthToolNamesFromStreamLine(line, reverseMap)
			if err != nil {
				t.Fatalf("restoreClaudeOAuthToolNamesFromStreamLine(%q) error = %v, want nil", name, err)
			}
			if got := gjson.GetBytes(helps.JSONPayload(restored), "content_block.name").String(); got != failOpenRealMCPTool {
				t.Fatalf("stream name restored as %q, want %q", got, failOpenRealMCPTool)
			}
		})
	}
}

// An unrecoverable name on the streaming path must leave the line byte-identical
// and return no error, so the stream survives and the client simply answers with
// an ordinary tool-not-found tool_result.
func TestClaudeMCPAliasFailOpenStreamLineSurvivesUnmappableName(t *testing.T) {
	server, _, reverseMap := failOpenRequest(t, "failopen-stream-unmappable-caller", failOpenRealMCPTool)

	unmappable := "mcp__" + server + "__abandon_no_such_tool_at_all"
	line := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}`, unmappable))
	restored, err := restoreClaudeOAuthToolNamesFromStreamLine(line, reverseMap)
	if err != nil {
		t.Fatalf("restoreClaudeOAuthToolNamesFromStreamLine() error = %v, want fail-open nil", err)
	}
	if !bytes.Equal(restored, line) {
		t.Fatalf("stream line = %s, want it forwarded unchanged", restored)
	}
}

// claudeMCPAliasRestoreError is no longer constructed by resolve, but it is kept
// as part of the executor's error-classification contract. Pin that it still
// exists and still classifies as request-scoped, so a future rebase that
// reintroduces a call site does not silently get credential-scoped behaviour.
func TestClaudeMCPAliasFailOpenRestoreErrorStaysRequestScoped(t *testing.T) {
	var requestErr cliproxyexecutor.RequestScopedError = claudeMCPAliasRestoreError{fmt.Errorf("probe")}
	if !requestErr.IsRequestScoped() {
		t.Fatal("claudeMCPAliasRestoreError.IsRequestScoped() = false, want true")
	}
}

// A request that declares BOTH a client tool and a caller MCP tool ending in the
// same component makes the drifted name ambiguous ACROSS the two namespaces. The
// pre-existing v7.2.142 alias paths resolve it to the client tool; the shape (b)
// passthrough recovery must not be allowed to preempt that and silently invoke
// the caller's MCP server instead. This pins the ordering, not just the outcome.
func TestClaudeMCPAliasFailOpenPassthroughRecoveryNeverPreemptsAliasMatch(t *testing.T) {
	secret := "failopen-cross-namespace"
	body := []byte(`{"tools":[{"name":"Bash","input_schema":{"type":"object"}},{"name":"mcp__shell__Bash"}]}`)
	remapped, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: secret})

	bashAlias := gjson.GetBytes(remapped, "tools.0.name").String()
	if bashAlias == "Bash" {
		t.Fatalf("client tool was not aliased")
	}
	if got := gjson.GetBytes(remapped, "tools.1.name").String(); got != "mcp__shell__Bash" {
		t.Fatalf("caller MCP tool mangled on the way out: %q", got)
	}

	// Exactly the drift v7.2.142 resolved to the client tool "Bash".
	drifted := "mcp__" + failOpenVirtualServer(t, secret) + "__Bash"
	if got := failOpenRestoreToolUse(t, drifted, reverseMap); got != "Bash" {
		t.Fatalf("cross-namespace drift restored as %q, want the client tool %q (v7.2.142 behaviour)", got, "Bash")
	}
	// And the exact alias still restores normally.
	if got := failOpenRestoreToolUse(t, bashAlias, reverseMap); got != "Bash" {
		t.Fatalf("exact alias restored as %q, want %q", got, "Bash")
	}
}

// The model may keep the real name's leading "mcp__" instead of dropping it.
// This is an exact lookup of a declared name, so it must restore, not fail open.
func TestClaudeMCPAliasFailOpenRecoversFullyQualifiedRealName(t *testing.T) {
	const real = "mcp__inventory__inventory_lookup_by_id"
	server, _, reverseMap := failOpenRequest(t, "failopen-fully-qualified", real)

	hybrid := "mcp__" + server + "__" + real // mcp__<virtual>__mcp__inventory__inventory_lookup_by_id
	if got := failOpenRestoreToolUse(t, hybrid, reverseMap); got != real {
		t.Fatalf("fully-qualified hybrid restored as %q, want %q", got, real)
	}
}
