package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeCursorSearchToolArguments_FilePathBecomesDirectoryAndGlob(t *testing.T) {
	got := normalizeCursorSearchToolArguments("rg", `{"path":"C:/repo/src/App.tsx","pattern":"ProductionLock","glob":""}`)

	if gjson.Get(got, "path").String() != "C:/repo/src" {
		t.Fatalf("path = %q, got args %s", gjson.Get(got, "path").String(), got)
	}
	if gjson.Get(got, "glob").String() != "App.tsx" {
		t.Fatalf("glob = %q, got args %s", gjson.Get(got, "glob").String(), got)
	}
}

func TestNormalizeCursorSearchToolArguments_DirectoryGlobBecomesRecursive(t *testing.T) {
	got := normalizeCursorSearchToolArguments("rg", `{"path":"C:/repo/src","pattern":"render\\(","glob":"*.test.tsx"}`)

	if gjson.Get(got, "path").String() != "C:/repo/src" {
		t.Fatalf("path changed unexpectedly: %s", got)
	}
	if gjson.Get(got, "glob").String() != "**/*.test.tsx" {
		t.Fatalf("glob = %q, got args %s", gjson.Get(got, "glob").String(), got)
	}
}

func TestNormalizeCursorSearchToolArguments_GlobPrefixMovesIntoPath(t *testing.T) {
	got := normalizeCursorSearchToolArguments("rg", `{"path":"C:\\repo","pattern":"production","glob":"server/**/*.js"}`)

	if gjson.Get(got, "path").String() != `C:\repo\server` {
		t.Fatalf("path = %q, got args %s", gjson.Get(got, "path").String(), got)
	}
	if gjson.Get(got, "glob").String() != "**/*.js" {
		t.Fatalf("glob = %q, got args %s", gjson.Get(got, "glob").String(), got)
	}
}

func TestNormalizeCursorSearchToolArguments_GlobPrefixAvoidsDuplicatePath(t *testing.T) {
	got := normalizeCursorSearchToolArguments("rg", `{"path":"C:\\repo\\server","pattern":"production","glob":"server/**/*.js"}`)

	if gjson.Get(got, "path").String() != `C:\repo\server` {
		t.Fatalf("path = %q, got args %s", gjson.Get(got, "path").String(), got)
	}
	if gjson.Get(got, "glob").String() != "**/*.js" {
		t.Fatalf("glob = %q, got args %s", gjson.Get(got, "glob").String(), got)
	}
}

func TestNormalizeCursorSearchToolArguments_RgBroadGlobIsCleared(t *testing.T) {
	got := normalizeCursorSearchToolArguments("rg", `{"path":"C:\\repo","pattern":"production","glob":"**/*"}`)

	if gjson.Get(got, "path").String() != `C:\repo` {
		t.Fatalf("path = %q, got args %s", gjson.Get(got, "path").String(), got)
	}
	if gjson.Get(got, "glob").String() != "" {
		t.Fatalf("glob = %q, got args %s", gjson.Get(got, "glob").String(), got)
	}
}

func TestNormalizeCursorSearchToolArguments_RgRelativePathUsesWorkspaceRoot(t *testing.T) {
	got := normalizeCursorSearchToolArgumentsWithWorkspace(
		"rg",
		`{"path":"src/components/ChatPanelNew","pattern":"EventSource","glob":"**/*.{ts,tsx,js,jsx}"}`,
		`C:\repo`,
	)

	if gjson.Get(got, "path").String() != `C:\repo\src\components\ChatPanelNew` {
		t.Fatalf("path = %q, got args %s", gjson.Get(got, "path").String(), got)
	}
}

func TestNormalizeCursorSearchToolArguments_GlobToolBroadPatternBecomesSpecific(t *testing.T) {
	got := normalizeCursorSearchToolArguments("Glob", `{"target_directory":"C:\\repo\\src","glob_pattern":"**/*"}`)

	if gjson.Get(got, "target_directory").String() != `C:\repo\src` {
		t.Fatalf("target_directory = %q, got args %s", gjson.Get(got, "target_directory").String(), got)
	}
	if gjson.Get(got, "glob_pattern").String() != cursorSearchSpecificSourceGlob() {
		t.Fatalf("glob_pattern = %q, got args %s", gjson.Get(got, "glob_pattern").String(), got)
	}
}

func TestNormalizeCursorSearchToolArguments_GlobToolPrefixMovesIntoTargetDirectory(t *testing.T) {
	got := normalizeCursorSearchToolArguments("Glob", `{"target_directory":"C:\\repo","glob_pattern":"server/**/*.js"}`)

	if gjson.Get(got, "target_directory").String() != `C:\repo\server` {
		t.Fatalf("target_directory = %q, got args %s", gjson.Get(got, "target_directory").String(), got)
	}
	if gjson.Get(got, "glob_pattern").String() != "**/*.js" {
		t.Fatalf("glob_pattern = %q, got args %s", gjson.Get(got, "glob_pattern").String(), got)
	}
}

func TestNormalizeCursorSearchToolArguments_SubagentDropsCloudBaseBranchOutsideCloud(t *testing.T) {
	got := normalizeCursorSearchToolArguments("Subagent", `{"environment":"local","cloud_base_branch":"main","prompt":"finish the task"}`)

	if gjson.Get(got, "environment").String() != "local" {
		t.Fatalf("environment = %q, got args %s", gjson.Get(got, "environment").String(), got)
	}
	if gjson.Get(got, "cloud_base_branch").Exists() {
		t.Fatalf("cloud_base_branch should be removed for non-cloud Subagent calls, got args %s", got)
	}
	if gjson.Get(got, "prompt").String() != "finish the task" {
		t.Fatalf("prompt changed unexpectedly, got args %s", got)
	}
}

func TestNormalizeCursorSearchToolArguments_SubagentDropsCloudBaseBranchWhenEnvironmentMissing(t *testing.T) {
	got := normalizeCursorSearchToolArguments("Subagent", `{"cloud_base_branch":"main","prompt":"finish the task"}`)

	if gjson.Get(got, "cloud_base_branch").Exists() {
		t.Fatalf("cloud_base_branch should be removed when Subagent environment is missing, got args %s", got)
	}
}

func TestNormalizeCursorSearchToolArguments_SubagentKeepsCloudBaseBranchForCloud(t *testing.T) {
	got := normalizeCursorSearchToolArguments("Subagent", `{"environment":"cloud","cloud_base_branch":"main","prompt":"finish the task"}`)

	if gjson.Get(got, "environment").String() != "cloud" {
		t.Fatalf("environment = %q, got args %s", gjson.Get(got, "environment").String(), got)
	}
	if gjson.Get(got, "cloud_base_branch").String() != "main" {
		t.Fatalf("cloud_base_branch = %q, got args %s", gjson.Get(got, "cloud_base_branch").String(), got)
	}
}

func TestConvertCodexResponseToOpenAI_SuppressesAndNormalizesRgArguments(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_rg","name":"rg"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected rg announcement chunk, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"path\":\"C:/repo/src/App.tsx\",\"pattern\":\"ProductionLock\",\"glob\":\"\"}"}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected rg argument delta to be suppressed until normalization, got %d chunks: %s", len(out), string(out[0]))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_rg","name":"rg","arguments":"{\"path\":\"C:/repo/src/App.tsx\",\"pattern\":\"ProductionLock\",\"glob\":\"\"}"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected normalized rg arguments chunk, got %d", len(out))
	}

	args := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.arguments").String()
	if gjson.Get(args, "path").String() != "C:/repo/src" {
		t.Fatalf("path = %q, args %s, chunk %s", gjson.Get(args, "path").String(), args, string(out[0]))
	}
	if gjson.Get(args, "glob").String() != "App.tsx" {
		t.Fatalf("glob = %q, args %s, chunk %s", gjson.Get(args, "glob").String(), args, string(out[0]))
	}
}

func TestConvertCodexResponseToOpenAI_SuppressesAndNormalizesGlobArguments(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_glob","name":"Glob"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected Glob announcement chunk, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"target_directory\":\"C:/repo/src\",\"glob_pattern\":\"**/*\"}"}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected Glob argument delta to be suppressed until normalization, got %d chunks: %s", len(out), string(out[0]))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_glob","name":"Glob","arguments":"{\"target_directory\":\"C:/repo/src\",\"glob_pattern\":\"**/*\"}"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected normalized Glob arguments chunk, got %d", len(out))
	}

	args := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.arguments").String()
	if gjson.Get(args, "target_directory").String() != "C:/repo/src" {
		t.Fatalf("target_directory = %q, args %s, chunk %s", gjson.Get(args, "target_directory").String(), args, string(out[0]))
	}
	if gjson.Get(args, "glob_pattern").String() != cursorSearchSpecificSourceGlob() {
		t.Fatalf("glob_pattern = %q, args %s, chunk %s", gjson.Get(args, "glob_pattern").String(), args, string(out[0]))
	}
}

func TestConvertCodexResponseToOpenAI_SuppressesAndNormalizesSubagentArguments(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_subagent","name":"Subagent"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected Subagent announcement chunk, got %d", len(out))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"environment\":\"local\",\"cloud_base_branch\":\"main\",\"prompt\":\"finish the task\"}"}`), &param)
	if len(out) != 0 {
		t.Fatalf("expected Subagent argument delta to be suppressed until normalization, got %d chunks: %s", len(out), string(out[0]))
	}

	out = ConvertCodexResponseToOpenAI(ctx, "gpt-5.5", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_subagent","name":"Subagent","arguments":"{\"environment\":\"local\",\"cloud_base_branch\":\"main\",\"prompt\":\"finish the task\"}"}}`), &param)
	if len(out) != 1 {
		t.Fatalf("expected normalized Subagent arguments chunk, got %d", len(out))
	}

	args := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.arguments").String()
	if gjson.Get(args, "environment").String() != "local" {
		t.Fatalf("environment = %q, args %s, chunk %s", gjson.Get(args, "environment").String(), args, string(out[0]))
	}
	if gjson.Get(args, "cloud_base_branch").Exists() {
		t.Fatalf("cloud_base_branch should be removed, args %s, chunk %s", args, string(out[0]))
	}
}
