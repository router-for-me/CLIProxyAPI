package chat_completions

import (
	"strings"
	"testing"
)

func TestSummarizeToolArgumentsShowsPathsButNotShellCommand(t *testing.T) {
	raw := []byte(`{"item":{"arguments":"{\"path\":\"C:\\\\Users\\\\me\\\\Documents\\\\report.txt\",\"pattern\":\"**/*.txt\",\"target_directory\":\"C:\\\\Users\\\\me\\\\repo\",\"glob_pattern\":\"server/**/*.js\",\"environment\":\"local\",\"cloud_base_branch\":\"main\",\"subagent_type\":\"general\",\"run_in_background\":true,\"model\":\"gpt-5.5\",\"description\":\"final delivery\",\"prompt\":\"private task details\",\"command\":\"Get-ChildItem -Recurse -Force C:\\\\Users\\\\me\"}"}}`)

	got := summarizeToolArguments(raw)
	if !strings.Contains(got, "path=C:\\Users\\me\\Documents\\report.txt") {
		t.Fatalf("summary should include path, got %q", got)
	}
	if !strings.Contains(got, "pattern=**/*.txt") {
		t.Fatalf("summary should include pattern, got %q", got)
	}
	if !strings.Contains(got, "target_directory=C:\\Users\\me\\repo") {
		t.Fatalf("summary should include target_directory, got %q", got)
	}
	if !strings.Contains(got, "glob_pattern=server/**/*.js") {
		t.Fatalf("summary should include glob_pattern, got %q", got)
	}
	if !strings.Contains(got, "environment=local") {
		t.Fatalf("summary should include Subagent environment, got %q", got)
	}
	if !strings.Contains(got, "cloud_base_branch=main") {
		t.Fatalf("summary should include Subagent cloud_base_branch, got %q", got)
	}
	if !strings.Contains(got, "subagent_type=general") {
		t.Fatalf("summary should include Subagent type, got %q", got)
	}
	if !strings.Contains(got, "run_in_background=true") {
		t.Fatalf("summary should include run_in_background, got %q", got)
	}
	if !strings.Contains(got, "model=gpt-5.5") {
		t.Fatalf("summary should include model, got %q", got)
	}
	if !strings.Contains(got, "description=final_delivery") {
		t.Fatalf("summary should include description, got %q", got)
	}
	if !strings.Contains(got, "command_len=") {
		t.Fatalf("summary should include command length, got %q", got)
	}
	if strings.Contains(got, "Get-ChildItem") {
		t.Fatalf("summary leaked shell command: %q", got)
	}
	if strings.Contains(got, "private task details") {
		t.Fatalf("summary leaked prompt: %q", got)
	}
}

func TestLastToolOutputFailureHintOnlyReportsFailures(t *testing.T) {
	failed := []byte(`{"messages":[{"role":"tool","content":[{"type":"text","text":"Error: Cannot find file C:\\\\Users\\\\me\\\\missing.txt"}]}]}`)
	if got := lastToolOutputClass(failed); got != "not-found" {
		t.Fatalf("class = %q, want not-found", got)
	}
	if got := lastToolOutputFailureHint(failed); !strings.Contains(got, "not-found:Error:_Cannot_find_file") {
		t.Fatalf("failure hint = %q", got)
	}

	success := []byte(`{"messages":[{"role":"tool","content":[{"type":"text","text":"private file contents"}]}]}`)
	if got := lastToolOutputFailureHint(success); got != "" {
		t.Fatalf("successful tool output should not be summarized, got %q", got)
	}

	sourceWithErrorWord := []byte(`{"messages":[{"role":"tool","content":[{"type":"text","text":"if err != nil { return fmt.Errorf(\"load failed: %w\", err) }"}]}]}`)
	if got := lastToolOutputClass(sourceWithErrorWord); got != "ok" {
		t.Fatalf("source content mentioning errors should be ok, got %q", got)
	}

	sourceWithNotFound := []byte(`{"messages":[{"role":"tool","content":[{"type":"text","text":"// The empty state says file not found, but this is source content."}]}]}`)
	if got := lastToolOutputClass(sourceWithNotFound); got != "ok" {
		t.Fatalf("source content mentioning not found should be ok, got %q", got)
	}

	sourceWithOutsideWorkspace := []byte(`{"messages":[{"role":"tool","content":[{"type":"text","text":"const help = 'outside workspace files are hidden';"}]}]}`)
	if got := lastToolOutputClass(sourceWithOutsideWorkspace); got != "ok" {
		t.Fatalf("source content mentioning outside workspace should be ok, got %q", got)
	}

	emptyGlob := []byte(`{"messages":[{"role":"tool","content":[{"type":"text","text":"<workspace_result> No_matches_found </workspace_result>"}]}]}`)
	if got := lastToolOutputClass(emptyGlob); got != "empty-glob" {
		t.Fatalf("empty glob class = %q, want empty-glob", got)
	}

	windowsPathError := []byte(`{"messages":[{"role":"tool","content":[{"type":"text","text":"Error running tool: rg: IO error for operation on : 系统找不到指定的路径。 (os error 3)"}]}]}`)
	if got := lastToolOutputClass(windowsPathError); got != "not-found" {
		t.Fatalf("windows path error class = %q, want not-found", got)
	}
}
