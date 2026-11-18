package usagestats

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
)

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatalf("marshal payload: %v", errMarshal)
	}
	return raw
}

func TestExtractWorkingDirFromClaudeSystemPrompt(t *testing.T) {
	const windowsDir = `C:\Users\me\repos\new-app`
	cases := map[string]struct {
		system any
		want   string
	}{
		"text blocks, current format": {
			system: []map[string]string{
				{"type": "text", "text": "You are Claude Code."},
				{"type": "text", "text": "# Environment\n - Primary working directory: " + windowsDir + "\n - Is a git repository: true"},
			},
			want: windowsDir,
		},
		"string prompt, legacy format": {
			system: "<env>\nWorking directory: /home/me/repos/app\nIs directory a git repo: Yes\n</env>",
			want:   "/home/me/repos/app",
		},
		"primary wins over additional directories": {
			system: "Primary working directory: /a/primary\nAdditional working directories:\n - /b/other",
			want:   "/a/primary",
		},
		"marker inside a sentence is ignored": {
			system: "Never change the Working directory: it is fixed.",
			want:   "",
		},
		"no marker": {
			system: "You are a helpful assistant.",
			want:   "",
		},
	}
	for name, tc := range cases {
		payload := mustJSON(t, map[string]any{
			"model":    "claude-opus-5-5",
			"system":   tc.system,
			"messages": []map[string]string{{"role": "user", "content": "hi"}},
		})
		if got := ExtractWorkingDir(payload); got != tc.want {
			t.Errorf("%s: ExtractWorkingDir = %q, want %q", name, got, tc.want)
		}
	}
}

// Claude Code 2.1 sends its environment block as a system-role message after
// the first user message, not in the top-level system prompt.
func TestExtractWorkingDirFromClaudeSystemRoleMessage(t *testing.T) {
	const dir = `C:\Users\me\.t3\worktrees\app\t3-1234`
	environment := "# Environment\nYou have been invoked in the following environment: \n - Primary working directory: " +
		dir + "\n - Is a git repository: true\n - Additional working directories:\n  - C:\\Users\\me\\other\n"
	payload := mustJSON(t, map[string]any{
		"model": "claude-opus-5-5",
		"system": []map[string]string{
			{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.322;"},
			{"type": "text", "text": "You are a Claude agent."},
		},
		"messages": []map[string]any{
			{"role": "user", "content": []map[string]string{
				{"type": "text", "text": "<system-reminder>Primary working directory: /decoy/in/user/content</system-reminder>"},
				{"type": "text", "text": "say ok"},
			}},
			{"role": "system", "content": []map[string]any{
				{"type": "text", "text": environment, "cache_control": map[string]string{"type": "ephemeral"}},
			}},
		},
	})
	if got := ExtractWorkingDir(payload); got != dir {
		t.Fatalf("ExtractWorkingDir = %q, want %q", got, dir)
	}

	// A system-role message with string content works too.
	plain := mustJSON(t, map[string]any{
		"messages": []map[string]any{
			{"role": "user", "content": "hi"},
			{"role": "system", "content": "Primary working directory: /home/me/repos/app\n"},
		},
	})
	if got := ExtractWorkingDir(plain); got != "/home/me/repos/app" {
		t.Fatalf("string content: ExtractWorkingDir = %q", got)
	}
}

func TestExtractWorkingDirOnlyScansTheStartOfTheConversation(t *testing.T) {
	messages := make([]map[string]any, 0, contextScanItems+1)
	for range contextScanItems {
		messages = append(messages, map[string]any{"role": "user", "content": "filler"})
	}
	messages = append(messages, map[string]any{"role": "system", "content": "Primary working directory: /too/late\n"})
	if got := ExtractWorkingDir(mustJSON(t, map[string]any{"messages": messages})); got != "" {
		t.Fatalf("ExtractWorkingDir = %q, want empty beyond the scan window", got)
	}
}

func TestExtractWorkingDirSkipsMentionsBeforeTheRealLine(t *testing.T) {
	system := "Paths are relative to the Primary working directory: keep that in mind.\n" +
		" - Primary working directory: /home/me/repos/app\n"
	if got := ExtractWorkingDir(mustJSON(t, map[string]any{"system": system})); got != "/home/me/repos/app" {
		t.Fatalf("ExtractWorkingDir = %q, want the line-start occurrence", got)
	}
}

func TestExtractWorkingDirIgnoresConversationContent(t *testing.T) {
	// A path quoted in the conversation is not the agent's environment.
	payload := mustJSON(t, map[string]any{
		"system": "You are a helpful assistant.",
		"messages": []map[string]string{
			{"role": "user", "content": "Primary working directory: /not/the/real/one"},
		},
	})
	if got := ExtractWorkingDir(payload); got != "" {
		t.Fatalf("ExtractWorkingDir = %q, want empty", got)
	}
}

func TestExtractWorkingDirFromCodexEnvironmentContext(t *testing.T) {
	environment := "<environment_context>\n  <cwd>/home/me/repos/service</cwd>\n  <shell>zsh</shell>\n</environment_context>"
	structured := mustJSON(t, map[string]any{
		"instructions": "You are Codex.",
		"input": []map[string]any{
			{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": environment}}},
			{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": "fix the bug"}}},
		},
	})
	if got := ExtractWorkingDir(structured); got != "/home/me/repos/service" {
		t.Errorf("structured content: ExtractWorkingDir = %q", got)
	}

	plain := mustJSON(t, map[string]any{
		"input": []map[string]any{{"role": "user", "content": environment}},
	})
	if got := ExtractWorkingDir(plain); got != "/home/me/repos/service" {
		t.Errorf("string content: ExtractWorkingDir = %q", got)
	}

	// A <cwd> tag outside an environment context is conversation content.
	quoted := mustJSON(t, map[string]any{
		"input": []map[string]any{{"role": "user", "content": "what does <cwd>/x</cwd> mean?"}},
	})
	if got := ExtractWorkingDir(quoted); got != "" {
		t.Errorf("quoted tag: ExtractWorkingDir = %q, want empty", got)
	}

	// So is a whole environment context quoted inside a question.
	asked := mustJSON(t, map[string]any{
		"input": []map[string]any{{"role": "user", "content": "explain this: " + environment}},
	})
	if got := ExtractWorkingDir(asked); got != "" {
		t.Errorf("quoted block: ExtractWorkingDir = %q, want empty", got)
	}
}

func TestExtractWorkingDirHandlesUnusablePayloads(t *testing.T) {
	oversized := mustJSON(t, map[string]any{"system": "Working directory: /" + strings.Repeat("x", maxWorkingDirLength)})
	for name, payload := range map[string][]byte{
		"empty":         nil,
		"not json":      []byte("not json at all"),
		"string input":  []byte(`{"input":"hello"}`),
		"oversized dir": oversized,
	} {
		if got := ExtractWorkingDir(payload); got != "" {
			t.Errorf("%s: ExtractWorkingDir = %q, want empty", name, got)
		}
	}
}

func TestProjectFromWorkingDir(t *testing.T) {
	cases := map[string]string{
		"":                              "",
		"/home/me/repos/app":            "app",
		"/home/me/repos/app/":           "app",
		`C:\Users\me\repos\CLIProxyAPI`: "CLIProxyAPI",
		`C:\Users\me\.t3\worktrees\CLIProxyAPI\t3-bacb3dc8-adcd500e58eb3fff`: "CLIProxyAPI",
		"/home/me/repos/app/.claude/worktrees/feature-x":                     "app",
		"/home/me/.codex/worktrees/1a2b/service":                             "service",
		`C:\Users\me\repos\app\.worktrees\feat-password`:                     "app",
		"/home/me/repos/app/.worktrees/fix/nested-branch":                    "app",
		"/srv/worktrees/thing":                                               "thing",
	}
	for dir, want := range cases {
		if got := ProjectFromWorkingDir(dir); got != want {
			t.Errorf("ProjectFromWorkingDir(%q) = %q, want %q", dir, got, want)
		}
	}
}
