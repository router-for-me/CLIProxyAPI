package claudemaster

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const gymTestSession = "12345678-1234-4123-8123-123456789abc"

func gymTestRecord(kind string, content any) map[string]any {
	return map[string]any{
		"type": kind, "sessionId": gymTestSession,
		"message": map[string]any{"role": kind, "content": content, "model": "claude-sonnet-5-5"},
	}
}

func gymTestCompleteRecords() []map[string]any {
	return []map[string]any{
		gymTestRecord("user", "This is Claude Master gym lane A. Then reply with GYM_LANE_A_OK."),
		gymTestRecord("assistant", []any{
			map[string]any{"type": "tool_use", "name": "Agent", "id": "first", "input": map[string]any{}},
			map[string]any{"type": "tool_use", "name": "Agent", "id": "second", "input": map[string]any{}},
		}),
		gymTestRecord("user", []any{
			map[string]any{"type": "tool_result", "tool_use_id": "first", "content": "module result"},
			map[string]any{"type": "tool_result", "tool_use_id": "second", "content": "command result"},
		}),
		gymTestRecord("assistant", []any{map[string]any{"type": "text", "text": "GYM_LANE_A_OK\nBoth agents finished."}}),
	}
}

func TestGymEvidenceRequiresAssistantAndTwoFinishedAgents(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("manual gym requires jq")
	}
	common, err := filepath.Abs("../../scripts/claude-master-gym/common.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"complete", "prompt-only", "stalled", "missing-result", "failed-result", "duplicate-result", "extra-agent", "extra-agent-after-marker", "nested-agent", "different-session", "new-unfinished-turn", "later-unrelated-turn", "marker-before-agents", "background-agents", "unknown-json", "missing-transcript"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			run := filepath.Join(home, "run.fixture")
			project := filepath.Join(home, ".claude", "projects", "-fixture")
			if err := os.MkdirAll(project, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(run, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(run, "lane-a.session-id"), []byte(gymTestSession+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			records := gymTestCompleteRecords()
			verified := name == "complete" || name == "nested-agent" || name == "later-unrelated-turn"
			switch name {
			case "prompt-only":
				records = records[:1]
			case "stalled":
				records = records[:2]
			case "missing-result":
				records[2]["message"].(map[string]any)["content"] = []any{map[string]any{"type": "tool_result", "tool_use_id": "first", "content": "result"}}
			case "failed-result":
				records[2]["message"].(map[string]any)["content"].([]any)[1].(map[string]any)["is_error"] = true
			case "duplicate-result":
				records[2]["message"].(map[string]any)["content"].([]any)[1].(map[string]any)["tool_use_id"] = "first"
			case "extra-agent", "extra-agent-after-marker", "nested-agent":
				extra := gymTestRecord("assistant", []any{map[string]any{"type": "tool_use", "name": "Agent", "id": "third", "input": map[string]any{}}})
				if name == "nested-agent" {
					extra["isSidechain"] = true
				}
				if name == "extra-agent" {
					records = append(records[:3], extra, records[3])
				} else {
					records = append(records, extra)
				}
			case "different-session":
				for _, record := range records {
					record["sessionId"] = "different-session"
				}
			case "new-unfinished-turn":
				records = append(records, gymTestRecord("user", "This is Claude Master gym lane A. Again reply with GYM_LANE_A_OK."))
			case "later-unrelated-turn":
				records = append(records, gymTestRecord("user", "Unrelated later task."), gymTestRecord("assistant", []any{map[string]any{"type": "tool_use", "name": "Agent", "id": "unrelated", "input": map[string]any{}}}))
			case "marker-before-agents":
				records = []map[string]any{records[0], records[3], records[1], records[2]}
			case "background-agents":
				records[1]["message"].(map[string]any)["content"].([]any)[0].(map[string]any)["input"] = map[string]any{"run_in_background": true}
			}
			var transcript strings.Builder
			for _, record := range records {
				raw, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				transcript.Write(raw)
				transcript.WriteByte('\n')
			}
			if name == "unknown-json" {
				transcript.WriteString("not-json-private-canary")
			}
			if name != "missing-transcript" {
				if err := os.WriteFile(filepath.Join(project, gymTestSession+".jsonl"), []byte(transcript.String()), 0600); err != nil {
					t.Fatal(err)
				}
			}
			child := exec.Command("bash", "-e", "-u", "-o", "pipefail", "-c", `source "$1"; gym_lane_evidence "$2" a`, "gym-evidence", common, run)
			child.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
			output, err := child.CombinedOutput()
			if err != nil || strings.Contains(string(output), "private-canary") || strings.Contains(string(output), "module result") {
				t.Fatalf("gym leaked raw transcript or failed: %v %s", err, output)
			}
			var evidence struct {
				Completion string `json:"completion"`
				Effort     bool   `json:"effort_verified"`
				Model      bool   `json:"model_verified"`
			}
			if err := json.Unmarshal(output, &evidence); err != nil {
				t.Fatal(err)
			}
			if (evidence.Completion == "verified") != verified || evidence.Effort {
				t.Fatalf("completion or self-reported effort was falsely verified: %s", output)
			}
			if verified && !evidence.Model {
				t.Fatal("authoritative message model was not checked")
			}
		})
	}
}

func TestGymConfigRemoteControlIsRecordedAndPassedToBothLanes(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("manual gym requires jq")
	}
	scripts, err := filepath.Abs("../../scripts/claude-master-gym")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "launcher")
	for path, script := range map[string]string{
		launcher:                     "#!/bin/sh\nprintf '<%s>\\n' \"$@\"\n",
		filepath.Join(bin, "go"):     "#!/bin/sh\nexec /bin/cp \"$CLAUDE_MASTER_TEST_LAUNCHER\" \"$3\"\n",
		filepath.Join(bin, "claude"): "#!/bin/sh\nprintf '2.1.999 (Claude Code)\\n'\n",
		filepath.Join(bin, "curl"):   "#!/bin/sh\nprintf '2.1.999\\n'\n",
		filepath.Join(bin, "tmux"):   "#!/bin/sh\n[ \"$1\" = has-session ] && exit 1\nexit 0\n",
	} {
		if err := os.WriteFile(path, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, profile := range []string{"gym-a", "gym-b"} {
		current := filepath.Join(dir, ".local", "share", "claude-master", "profiles", profile, "current")
		if err := os.MkdirAll(filepath.Join(current, "auth"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(current, "profile.json"), []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(dir, "gym.env")
	if err := os.WriteFile(config, []byte("CLAUDE_MASTER_GYM_LANE_A_PROFILES=gym-a\nCLAUDE_MASTER_GYM_LANE_B_PROFILES=gym-b\nCLAUDE_MASTER_GYM_REMOTE_CONTROL=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_MASTER_TEST_LAUNCHER", launcher)
	state := filepath.Join(dir, "state")
	t.Setenv("CLAUDE_MASTER_GYM_STATE_DIR", state)
	// Fake tmux creates no process; drive then correctly reports that no session
	// is running. The completed startup artifacts remain available for inspection.
	output, err := exec.Command("bash", filepath.Join(scripts, "start.sh"), "--config", config).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Gym tmux session is not running") {
		t.Fatalf("synthetic startup did not reach the expected driver boundary: %v %s", err, output)
	}
	raw, err := os.ReadFile(filepath.Join(state, "current"))
	if err != nil {
		t.Fatal(err)
	}
	run := strings.TrimSpace(string(raw))
	if raw, err := os.ReadFile(filepath.Join(run, "remote-control")); err != nil || string(raw) != "1\n" {
		t.Fatal("unexported config Remote Control was not recorded")
	}
	// Deliberately contradict the original config environment. Recorded run
	// options must win even when an existing tmux server has stale environment.
	t.Setenv("CLAUDE_MASTER_GYM_REMOTE_CONTROL", "0")
	for _, lane := range []string{"a", "b"} {
		output, err := exec.Command("bash", filepath.Join(scripts, "run-lane.sh"), run, lane).CombinedOutput()
		if err != nil || !strings.Contains(string(output), "<--remote-control=claude-master-gym-"+lane+">") || !strings.Contains(string(output), "<--session-id>") {
			t.Fatalf("lane lost recorded Remote Control/session settings: %v %s", err, output)
		}
	}
}
