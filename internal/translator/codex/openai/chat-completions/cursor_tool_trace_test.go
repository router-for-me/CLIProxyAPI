package chat_completions

import (
	"strings"
	"testing"
)

func TestSummarizeToolArgumentsShowsPathsButNotShellCommand(t *testing.T) {
	raw := []byte(`{"item":{"arguments":"{\"path\":\"C:\\\\Users\\\\me\\\\Documents\\\\report.txt\",\"pattern\":\"**/*.txt\",\"command\":\"Get-ChildItem -Recurse -Force C:\\\\Users\\\\me\"}"}}`)

	got := summarizeToolArguments(raw)
	if !strings.Contains(got, "path=C:\\Users\\me\\Documents\\report.txt") {
		t.Fatalf("summary should include path, got %q", got)
	}
	if !strings.Contains(got, "pattern=**/*.txt") {
		t.Fatalf("summary should include pattern, got %q", got)
	}
	if !strings.Contains(got, "command_len=") {
		t.Fatalf("summary should include command length, got %q", got)
	}
	if strings.Contains(got, "Get-ChildItem") {
		t.Fatalf("summary leaked shell command: %q", got)
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
}
