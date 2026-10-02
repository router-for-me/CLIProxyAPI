package responses

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildClaudeToolNames_RoundTripAndStability(t *testing.T) {
	reqJSON := []byte(`{
		"tools": [
			{"type": "function", "name": "normal_tool"},
			{"type": "function", "name": "very_long_tool_name_that_exceeds_sixty_four_characters_and_needs_truncation_alpha"},
			{"type": "function", "name": "very_long_tool_name_that_exceeds_sixty_four_characters_and_needs_truncation_beta"},
			{"type": "custom", "name": "custom.tool.with.dots"},
			{"type": "namespace", "name": "mcp__my_server", "tools": [
				{"type": "function", "name": "get_something"}
			]}
		],
		"input": [
			{"type": "function_call", "name": "history_only_tool_name_that_is_also_very_long_exceeding_sixty_four_characters"}
		]
	}`)

	root := gjson.ParseBytes(reqJSON)
	names := buildClaudeToolNames(root)

	// Verify all declared tools are unique and match pattern
	seenClaudeNames := map[string]string{}
	for id, claudeName := range names.toClaude {
		if !claudeToolNamePattern.MatchString(claudeName) {
			t.Fatalf("claude name %q for identity %q does not match pattern", claudeName, id)
		}
		if len(claudeName) > 64 {
			t.Fatalf("claude name %q length = %d > 64", claudeName, len(claudeName))
		}
		if existingID, exists := seenClaudeNames[claudeName]; exists && existingID != id {
			t.Fatalf("claude name %q collided between %q and %q", claudeName, existingID, id)
		}
		seenClaudeNames[claudeName] = id

		// Verify reversibility
		revID := names.identity(claudeName)
		if revID != id {
			t.Fatalf("identity(%q) = %q, want %q", claudeName, revID, id)
		}
	}

	// Normal tool should keep its name
	if names.claudeName("normal_tool") != "normal_tool" {
		t.Fatalf("claudeName(normal_tool) = %q, want normal_tool", names.claudeName("normal_tool"))
	}

	// Namespace child qualified name
	qualified := "mcp__my_server__get_something"
	if names.claudeName(qualified) != qualified {
		t.Fatalf("claudeName(%q) = %q, want %q", qualified, names.claudeName(qualified), qualified)
	}

	// Alpha and Beta long tools must not have the same claude name
	alphaID := "very_long_tool_name_that_exceeds_sixty_four_characters_and_needs_truncation_alpha"
	betaID := "very_long_tool_name_that_exceeds_sixty_four_characters_and_needs_truncation_beta"
	if names.claudeName(alphaID) == names.claudeName(betaID) {
		t.Fatalf("alpha and beta long tools received same name: %q", names.claudeName(alphaID))
	}
}

func TestBuildClaudeToolNames_DeclarationOrderInvariance(t *testing.T) {
	tool1 := "mcp__example_apps__acme_inventory_service__acme_inventory_service_get_item_prices"
	tool2 := "mcp__example_apps__acme_inventory_service__acme_inventory_service_get_item_metrics"

	reqA := []byte(`{
		"tools": [
			{"type": "function", "name": "mcp__example_apps__acme_inventory_service__acme_inventory_service_get_item_prices"},
			{"type": "function", "name": "mcp__example_apps__acme_inventory_service__acme_inventory_service_get_item_metrics"}
		]
	}`)
	reqB := []byte(`{
		"tools": [
			{"type": "function", "name": "mcp__example_apps__acme_inventory_service__acme_inventory_service_get_item_metrics"},
			{"type": "function", "name": "mcp__example_apps__acme_inventory_service__acme_inventory_service_get_item_prices"}
		]
	}`)

	namesA := buildClaudeToolNames(gjson.ParseBytes(reqA))
	namesB := buildClaudeToolNames(gjson.ParseBytes(reqB))

	if namesA.claudeName(tool1) != namesB.claudeName(tool1) {
		t.Fatalf("order affected tool1 mapping: %q vs %q", namesA.claudeName(tool1), namesB.claudeName(tool1))
	}
	if namesA.claudeName(tool2) != namesB.claudeName(tool2) {
		t.Fatalf("order affected tool2 mapping: %q vs %q", namesA.claudeName(tool2), namesB.claudeName(tool2))
	}
}

func TestBuildClaudeToolNames_SingleLongName(t *testing.T) {
	longTool := "a_single_very_long_tool_name_that_exceeds_sixty_four_characters_cleanly"
	reqJSON := []byte(`{
		"tools": [
			{"type": "function", "name": "a_single_very_long_tool_name_that_exceeds_sixty_four_characters_cleanly"}
		]
	}`)

	names := buildClaudeToolNames(gjson.ParseBytes(reqJSON))
	claudeName := names.claudeName(longTool)
	if len(claudeName) > 64 {
		t.Fatalf("claudeName length = %d > 64: %q", len(claudeName), claudeName)
	}
	if !claudeToolNamePattern.MatchString(claudeName) {
		t.Fatalf("claudeName %q does not match pattern", claudeName)
	}
	if names.identity(claudeName) != longTool {
		t.Fatalf("identity(%q) = %q, want %q", claudeName, names.identity(claudeName), longTool)
	}
}

func TestBuildClaudeToolNames_CustomToolCollision(t *testing.T) {
	tool1 := "custom__long_namespace_path_exceeding_sixty_four_characters_long__operation_query_v1_execute_handler"
	tool2 := "custom__long_namespace_path_exceeding_sixty_four_characters_long__operation_query_v1_execute_stream"

	reqJSON := []byte(fmt.Sprintf(`{
		"tools": [
			{"type": "custom", "name": %q},
			{"type": "custom", "name": %q}
		]
	}`, tool1, tool2))

	root := gjson.ParseBytes(reqJSON)
	names := buildClaudeToolNames(root)
	customMap := responsesCustomToolNames(reqJSON)

	cName1 := names.claudeName(tool1)
	cName2 := names.claudeName(tool2)

	if cName1 == cName2 {
		t.Fatalf("custom tools collided: %q", cName1)
	}
	if len(cName1) > 64 || len(cName2) > 64 {
		t.Fatalf("custom tool claude names must not exceed 64 chars: %d, %d", len(cName1), len(cName2))
	}
	if _, ok := customMap[cName1]; !ok {
		t.Fatalf("customMap missing claudeName1 %q", cName1)
	}
	if _, ok := customMap[cName2]; !ok {
		t.Fatalf("customMap missing claudeName2 %q", cName2)
	}
	if names.identity(cName1) != tool1 {
		t.Fatalf("identity(%q) = %q, want %q", cName1, names.identity(cName1), tool1)
	}
	if names.identity(cName2) != tool2 {
		t.Fatalf("identity(%q) = %q, want %q", cName2, names.identity(cName2), tool2)
	}

	// Verify non-stream response restores the custom tool call type and original name
	claudeResp := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_test_custom","usage":{"input_tokens":10,"output_tokens":5}}}`,
		fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_custom_0","name":%q,"input":{"input":"query_payload"}}}`, cName1),
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))
	translatedResp := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-sonnet-5-5", reqJSON, nil, claudeResp, nil)
	outputItem := gjson.GetBytes(translatedResp, "output.0")
	if outputItem.Get("type").String() != "custom_tool_call" {
		t.Fatalf("output.0.type = %q, want custom_tool_call", outputItem.Get("type").String())
	}
	if outputItem.Get("name").String() != tool1 {
		t.Fatalf("output.0.name = %q, want %q", outputItem.Get("name").String(), tool1)
	}
}

func TestBuildClaudeToolNames_DeclaredToolsPrecedeHistory(t *testing.T) {
	// A declared tool has a name that collides with a history tool
	reqJSON := []byte(`{
		"tools": [
			{"type": "function", "name": "tool_x"}
		],
		"input": [
			{"type": "function_call", "name": "tool.x"}
		]
	}`)

	names := buildClaudeToolNames(gjson.ParseBytes(reqJSON))

	// tool_x must keep its exact name
	if names.claudeName("tool_x") != "tool_x" {
		t.Fatalf("declared tool_x was stolen: %q", names.claudeName("tool_x"))
	}

	// tool.x (which sanitizes to tool_x) must be disambiguated with hash because tool_x is taken
	if names.claudeName("tool.x") == "tool_x" {
		t.Fatalf("history tool.x unexpectedly got tool_x instead of disambiguated name")
	}
	if !strings.HasPrefix(names.claudeName("tool.x"), "tool_x_") {
		t.Fatalf("history tool.x = %q, want prefix tool_x_", names.claudeName("tool.x"))
	}
}
