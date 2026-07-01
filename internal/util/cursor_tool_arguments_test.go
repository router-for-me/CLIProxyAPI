package util

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestExtractCursorWorkspaceRootFromToolOutput(t *testing.T) {
	raw := []byte(`{
		"input":[
			{"type":"function_call_output","output":[{"type":"text","text":"<workspace_result workspace_path=\"c:\\Users\\me\\repo\"> No_matches_found </workspace_result>"}]}
		]
	}`)

	if got := ExtractCursorWorkspaceRoot(raw); got != `c:\Users\me\repo` {
		t.Fatalf("workspace root = %q", got)
	}
}

func TestNormalizeCursorToolArguments_RgRelativePathUsesWorkspaceRoot(t *testing.T) {
	got := NormalizeCursorToolArguments(
		"rg",
		`{"path":"src/components/ChatPanelNew","pattern":"EventSource","glob":"**/*.{ts,tsx,js,jsx}"}`,
		`C:\Users\me\repo`,
	)

	if gjson.Get(got, "path").String() != `C:\Users\me\repo\src\components\ChatPanelNew` {
		t.Fatalf("path = %q, got args %s", gjson.Get(got, "path").String(), got)
	}
	if gjson.Get(got, "glob").String() != "**/*.{ts,tsx,js,jsx}" {
		t.Fatalf("glob changed unexpectedly: %s", got)
	}
}

func TestNormalizeCursorToolArguments_RgEmptyPathUsesWorkspaceRoot(t *testing.T) {
	got := NormalizeCursorToolArguments(
		"rg",
		`{"path":"","pattern":"ProductionIntentProposalCard","glob":"**/*.{ts,tsx,js,jsx,md}"}`,
		`C:\Users\me\repo`,
	)

	if gjson.Get(got, "path").String() != `C:\Users\me\repo` {
		t.Fatalf("path = %q, got args %s", gjson.Get(got, "path").String(), got)
	}
}

func TestNormalizeCursorToolArguments_ReadFileRelativePathUsesWorkspaceRoot(t *testing.T) {
	got := NormalizeCursorToolArguments(
		"ReadFile",
		`{"path":"src/components/ChatPanelNew.tsx"}`,
		`C:\Users\me\repo`,
	)

	if gjson.Get(got, "path").String() != `C:\Users\me\repo\src\components\ChatPanelNew.tsx` {
		t.Fatalf("path = %q, got args %s", gjson.Get(got, "path").String(), got)
	}
}

func TestNormalizeCursorToolArguments_GlobRelativeTargetUsesWorkspaceRoot(t *testing.T) {
	got := NormalizeCursorToolArguments(
		"Glob",
		`{"target_directory":"src","glob_pattern":"**/*Chat*.{tsx,ts,js,jsx}"}`,
		`C:\Users\me\repo`,
	)

	if gjson.Get(got, "target_directory").String() != `C:\Users\me\repo\src` {
		t.Fatalf("target_directory = %q, got args %s", gjson.Get(got, "target_directory").String(), got)
	}
}

func TestExtractCursorWorkspaceRootIsTextBased(t *testing.T) {
	raw := []byte(`{"input":[{"type":"function_call_output","output":[{"type":"text","text":"<workspace_result workspace_path=\"c:/repo\"> ok </workspace_result>"}]}]}`)

	if got := ExtractCursorWorkspaceRoot(raw); got != "c:/repo" {
		t.Fatalf("workspace root parser should read workspace_result text, got %q", got)
	}

	if gjson.Get(NormalizeCursorToolArguments("Subagent", `{"environment":"local","cloud_base_branch":"main","prompt":"secret"}`, ""), "cloud_base_branch").Exists() {
		t.Fatal("subagent cloud_base_branch was not removed")
	}
}
