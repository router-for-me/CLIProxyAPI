package responses

import (
	"strings"
	"testing"
)

func TestSummarizeToolArgumentsShowsPathsButNotShellCommand(t *testing.T) {
	raw := []byte(`{"item":{"input":"{\"file_path\":\"C:\\\\Users\\\\me\\\\Documents\\\\report.txt\",\"query\":\"生成报告.txt\",\"command\":\"Get-Content C:\\\\Users\\\\me\\\\Documents\\\\report.txt\"}"}}`)

	got := summarizeToolArguments(raw)
	if !strings.Contains(got, "file_path=C:\\Users\\me\\Documents\\report.txt") {
		t.Fatalf("summary should include file_path, got %q", got)
	}
	if !strings.Contains(got, "query=生成报告.txt") {
		t.Fatalf("summary should include query, got %q", got)
	}
	if !strings.Contains(got, "command_len=") {
		t.Fatalf("summary should include command length, got %q", got)
	}
	if strings.Contains(got, "Get-Content") {
		t.Fatalf("summary leaked shell command: %q", got)
	}
}
