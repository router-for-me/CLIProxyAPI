package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

func TestCommandCodeExecutor_Identifier(t *testing.T) {
	e := NewCommandCodeExecutor(nil)
	if got := e.Identifier(); got != "commandcode" {
		t.Fatalf("Identifier() = %q, want %q", got, "commandcode")
	}
}

func TestMessagesToQuery_SingleUserMessage(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	got := messagesToQuery(payload)
	if got != "hello" {
		t.Fatalf("messagesToQuery() = %q, want %q", got, "hello")
	}
}

func TestMessagesToQuery_MultiTurn(t *testing.T) {
	payload := []byte(`{"messages":[
		{"role":"system","content":"You are helpful"},
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"hello"},
		{"role":"user","content":"bye"}
	]}`)
	got := messagesToQuery(payload)
	want := "System: You are helpful\n\nhi\n\nAssistant: hello\n\nbye"
	if got != want {
		t.Fatalf("messagesToQuery() = %q, want %q", got, want)
	}
}

func TestMessagesToQuery_ArrayContent(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"part1"},{"type":"text","text":"part2"}]}]}`)
	got := messagesToQuery(payload)
	if got != "part1\npart2" {
		t.Fatalf("messagesToQuery() = %q, want %q", got, "part2")
	}
}

func TestMessagesToQuery_EmptyMessages(t *testing.T) {
	payload := []byte(`{"messages":[]}`)
	got := messagesToQuery(payload)
	if got != "" {
		t.Fatalf("messagesToQuery() = %q, want empty", got)
	}
}

func TestMessagesToQuery_NoMessagesKey(t *testing.T) {
	payload := []byte(`{"model":"test"}`)
	got := messagesToQuery(payload)
	if got != string(payload) {
		t.Fatalf("messagesToQuery() = %q, want %q", got, string(payload))
	}
}

func TestExtractMessageContent_String(t *testing.T) {
	payload := []byte(`{"content":"hello"}`)
	result := gjson.GetBytes(payload, "content")
	got := extractMessageContent(result)
	if got != "hello" {
		t.Fatalf("extractMessageContent() = %q, want %q", got, "hello")
	}
}

func TestExtractMessageContent_Array(t *testing.T) {
	payload := []byte(`{"content":[{"type":"text","text":"a"},{"type":"image","text":"b"},{"type":"text","text":"c"}]}`)
	result := gjson.GetBytes(payload, "content")
	got := extractMessageContent(result)
	if got != "a\nc" {
		t.Fatalf("extractMessageContent() = %q, want %q", got, "a\nc")
	}
}

func TestExtractMessageContent_Missing(t *testing.T) {
	payload := []byte(`{}`)
	result := gjson.GetBytes(payload, "content")
	got := extractMessageContent(result)
	if got != "" {
		t.Fatalf("extractMessageContent() = %q, want empty", got)
	}
}

func TestBuildChatCompletionResponse(t *testing.T) {
	data := buildChatCompletionResponse("test-model", "hello world", "")

	var resp map[string]any
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if resp["object"] != "chat.completion" {
		t.Fatalf("object = %v, want chat.completion", resp["object"])
	}
	if resp["model"] != "test-model" {
		t.Fatalf("model = %v, want test-model", resp["model"])
	}

	choices := resp["choices"].([]any)
	choice := choices[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	if msg["role"] != "assistant" {
		t.Fatalf("role = %v, want assistant", msg["role"])
	}
	if msg["content"] != "hello world" {
		t.Fatalf("content = %v, want hello world", msg["content"])
	}
	if choice["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %v, want stop", choice["finish_reason"])
	}

	usage := resp["usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(0) {
		t.Fatalf("prompt_tokens = %v, want 0", usage["prompt_tokens"])
	}

	_, hasSessionID := resp["session_id"]
	if hasSessionID {
		t.Error("response should not have session_id field when empty")
	}
}

func TestBuildStreamRoleChunk(t *testing.T) {
	data := buildStreamRoleChunk("chatcmpl-test", "test-model", "")
	if len(data) == 0 {
		t.Fatal("empty chunk")
	}
	if string(data[:6]) != "data: " {
		t.Fatalf("prefix = %q, want 'data: '", string(data[:6]))
	}

	var chunk map[string]any
	if err := json.Unmarshal(data[6:], &chunk); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if chunk["object"] != "chat.completion.chunk" {
		t.Fatalf("object = %v, want chat.completion.chunk", chunk["object"])
	}

	choices := chunk["choices"].([]any)
	choice := choices[0].(map[string]any)
	delta := choice["delta"].(map[string]any)
	if delta["role"] != "assistant" {
		t.Fatalf("delta.role = %v, want assistant", delta["role"])
	}

	_, hasSessionID := chunk["session_id"]
	if hasSessionID {
		t.Error("chunk should not have session_id field when empty")
	}
}

func TestBuildStreamContentChunk(t *testing.T) {
	data := buildStreamContentChunk("chatcmpl-test", "test-model", "hello")
	var chunk map[string]any
	if err := json.Unmarshal(data[6:], &chunk); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	choices := chunk["choices"].([]any)
	choice := choices[0].(map[string]any)
	delta := choice["delta"].(map[string]any)
	if delta["content"] != "hello" {
		t.Fatalf("delta.content = %v, want hello", delta["content"])
	}
}

func TestBuildStreamFinishChunk(t *testing.T) {
	data := buildStreamFinishChunk("chatcmpl-test", "test-model")
	var chunk map[string]any
	if err := json.Unmarshal(data[6:], &chunk); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	choices := chunk["choices"].([]any)
	choice := choices[0].(map[string]any)
	if choice["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %v, want stop", choice["finish_reason"])
	}
}

func TestBuildArgs_WithModel(t *testing.T) {
	e := NewCommandCodeExecutor(nil)
	args := e.buildArgs("test query", "deepseek/deepseek-v4-pro", "")
	if args[0] != "-p" || args[1] != "test query" {
		t.Fatalf("args start = %v, want [-p test query]", args[:2])
	}
	if args[2] != "-m" || args[3] != "deepseek/deepseek-v4-pro" {
		t.Fatalf("model args = %v, want [-m deepseek/deepseek-v4-pro]", args[2:4])
	}
}

func TestBuildArgs_WithConfig(t *testing.T) {
	cfg := &config.Config{
		CommandCodeKey: []config.CommandCodeKey{
			{
				DefaultModel:   "claude-sonnet-4-6",
				MaxTurns:       5,
				AutoAccept:     true,
				PermissionMode: "trust",
			},
		},
	}
	e := NewCommandCodeExecutor(cfg)
	args := e.buildArgs("query", "", "")
	// -p query -m claude-sonnet-4-6 --max-turns 5 --auto-accept --permission-mode trust
	if args[0] != "-p" || args[1] != "query" {
		t.Fatalf("args start = %v", args[:2])
	}
	if args[2] != "-m" || args[3] != "claude-sonnet-4-6" {
		t.Fatalf("model args = %v", args[2:4])
	}
	found := false
	for _, a := range args {
		if a == "--auto-accept" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing --auto-accept in args: %v", args)
	}
}

func TestBuildArgs_RequestModelOverridesConfig(t *testing.T) {
	cfg := &config.Config{
		CommandCodeKey: []config.CommandCodeKey{
			{DefaultModel: "default-model"},
		},
	}
	e := NewCommandCodeExecutor(cfg)
	args := e.buildArgs("query", "custom-model", "")
	if args[2] != "-m" || args[3] != "custom-model" {
		t.Fatalf("expected request model to override config, got args: %v", args[2:4])
	}
}

func TestBuildArgs_Yolo(t *testing.T) {
	cfg := &config.Config{
		CommandCodeKey: []config.CommandCodeKey{
			{Yolo: true},
		},
	}
	e := NewCommandCodeExecutor(cfg)
	args := e.buildArgs("query", "", "")
	found := false
	for _, a := range args {
		if a == "--yolo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing --yolo in args: %v", args)
	}
}

func TestResolveConfig_ReturnsFirstEntry(t *testing.T) {
	cfg := &config.Config{
		CommandCodeKey: []config.CommandCodeKey{
			{DefaultModel: "first"},
			{DefaultModel: "second"},
		},
	}
	e := NewCommandCodeExecutor(cfg)
	got := e.resolveConfig()
	if got == nil || got.DefaultModel != "first" {
		t.Fatalf("resolveConfig() = %v, want first entry", got)
	}
}

func TestResolveConfig_NilConfig(t *testing.T) {
	e := NewCommandCodeExecutor(nil)
	if got := e.resolveConfig(); got != nil {
		t.Fatalf("resolveConfig() = %v, want nil", got)
	}
}

func TestResolveConfig_EmptyConfig(t *testing.T) {
	e := NewCommandCodeExecutor(&config.Config{})
	if got := e.resolveConfig(); got != nil {
		t.Fatalf("resolveConfig() = %v, want nil", got)
	}
}

func TestResolveBinaryPath_FallsBackToCmd(t *testing.T) {
	e := NewCommandCodeExecutor(nil)
	path := e.resolveBinaryPath()
	if path == "" {
		t.Fatal("resolveBinaryPath() returned empty")
	}
}

func TestExtractSessionID_FromPayload(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		expected string
	}{
		{
			name:     "with session_id",
			payload:  `{"session_id":"abc-123","messages":[{"role":"user","content":"hello"}]}`,
			expected: "abc-123",
		},
		{
			name:     "with whitespace",
			payload:  `{"session_id":"  xyz-789  ","messages":[{"role":"user","content":"hi"}]}`,
			expected: "xyz-789",
		},
		{
			name:     "without session_id",
			payload:  `{"messages":[{"role":"user","content":"hello"}]}`,
			expected: "",
		},
		{
			name:     "empty session_id",
			payload:  `{"session_id":"","messages":[{"role":"user","content":"hello"}]}`,
			expected: "",
		},
		{
			name:     "with other fields",
			payload:  `{"model":"test","session_id":"my-session","messages":[{"role":"user","content":"hi"}]}`,
			expected: "my-session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractSessionID([]byte(tt.payload))
			if result != tt.expected {
				t.Errorf("extractSessionID() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestSnapshotSessionDir(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, ".commandcode", "projects")

	err := os.MkdirAll(sessionDir, 0755)
	if err != nil {
		t.Fatalf("create session dir: %v", err)
	}

	snap, err := snapshotSessionDir(tmpDir)
	if err != nil {
		t.Fatalf("snapshotSessionDir() = %v", err)
	}

	if len(snap.entries) != 0 {
		t.Errorf("empty session dir should have no entries, got %d", len(snap.entries))
	}

	_, err = os.Create(filepath.Join(sessionDir, "test1.jsonl"))
	if err != nil {
		t.Fatalf("create session file: %v", err)
	}

	_, err = os.Create(filepath.Join(sessionDir, "test2.jsonl"))
	if err != nil {
		t.Fatalf("create session file: %v", err)
	}

	snap2, err := snapshotSessionDir(tmpDir)
	if err != nil {
		t.Fatalf("snapshotSessionDir() = %v", err)
	}

	if len(snap2.entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(snap2.entries))
	}

	if snap2.entries["test1.jsonl"].IsZero() {
		t.Error("test1.jsonl has zero mod time")
	}
	if snap2.entries["test2.jsonl"].IsZero() {
		t.Error("test2.jsonl has zero mod time")
	}
}

func TestDetectNewSession_FindsNewFile(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, ".commandcode", "projects")

	err := os.MkdirAll(sessionDir, 0755)
	if err != nil {
		t.Fatalf("create session dir: %v", err)
	}

	snap, err := snapshotSessionDir(tmpDir)
	if err != nil {
		t.Fatalf("snapshotSessionDir() = %v", err)
	}

	sessionID, ok := detectNewSession(tmpDir, snap)
	if ok {
		t.Errorf("detectNewSession() returned ok=true with no files, want false")
	}
	if sessionID != "" {
		t.Errorf("detectNewSession() = %q, want empty", sessionID)
	}

	_, err = os.Create(filepath.Join(sessionDir, "new-session-12345.jsonl"))
	if err != nil {
		t.Fatalf("create session file: %v", err)
	}

	sessionID, ok = detectNewSession(tmpDir, snap)
	if !ok {
		t.Error("detectNewSession() returned ok=false with new file, want true")
	}
	if sessionID != "new-session-12345" {
		t.Errorf("detectNewSession() = %q, want 'new-session-12345'", sessionID)
	}
}

func TestDetectNewSession_NoChanges(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, ".commandcode", "projects")

	err := os.MkdirAll(sessionDir, 0755)
	if err != nil {
		t.Fatalf("create session dir: %v", err)
	}

	_, err = os.Create(filepath.Join(sessionDir, "old-session-123.jsonl"))
	if err != nil {
		t.Fatalf("create session file: %v", err)
	}

	snap, err := snapshotSessionDir(tmpDir)
	if err != nil {
		t.Fatalf("snapshotSessionDir() = %v", err)
	}

	// Create a new file that should be detected as new
	_, err = os.Create(filepath.Join(sessionDir, "new-session-456.jsonl"))
	if err != nil {
		t.Fatalf("create new session file: %v", err)
	}

	sessionID, ok := detectNewSession(tmpDir, snap)
	if !ok {
		t.Error("detectNewSession() returned ok=false with new file, want true")
	}
	if sessionID != "new-session-456" {
		t.Errorf("detectNewSession() = %q, want 'new-session-456'", sessionID)
	}
}

func TestDetectNewSession_MultipleSessions(t *testing.T) {
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, ".commandcode", "projects")

	err := os.MkdirAll(sessionDir, 0755)
	if err != nil {
		t.Fatalf("create session dir: %v", err)
	}

	_, err = os.Create(filepath.Join(sessionDir, "session-a.jsonl"))
	if err != nil {
		t.Fatalf("create session file: %v", err)
	}

	snap, err := snapshotSessionDir(tmpDir)
	if err != nil {
		t.Fatalf("snapshotSessionDir() = %v", err)
	}

	_, err = os.Create(filepath.Join(sessionDir, "session-b.jsonl"))
	if err != nil {
		t.Fatalf("create session file: %v", err)
	}

	sessionID, ok := detectNewSession(tmpDir, snap)
	if !ok {
		t.Error("detectNewSession() returned ok=false with new file, want true")
	}
	if sessionID != "session-b" {
		t.Errorf("detectNewSession() = %q, want 'session-b'", sessionID)
	}
}

func TestBuildArgs_WithSessionID(t *testing.T) {
	e := NewCommandCodeExecutor(nil)
	args := e.buildArgs("test query", "test-model", "resume-session-123")

	if args[0] != "-p" || args[1] != "test query" {
		t.Fatalf("args start = %v, want [-p test query]", args[:2])
	}

	foundResume := false
	for _, a := range args {
		if a == "--resume" {
			foundResume = true
		}
	}
	if !foundResume {
		t.Fatalf("missing --resume in args: %v", args)
	}

	if args[len(args)-1] != "resume-session-123" {
		t.Errorf("session ID not at end: %v", args[len(args)-1])
	}
}

func TestBuildArgs_WithoutSessionID(t *testing.T) {
	e := NewCommandCodeExecutor(nil)
	args := e.buildArgs("test query", "test-model", "")
	if args[0] != "-p" || args[1] != "test query" {
		t.Fatalf("args start = %v, want [-p test query]", args[:2])
	}

	foundResume := false
	for _, a := range args {
		if a == "--resume" {
			foundResume = true
		}
	}
	if foundResume {
		t.Errorf("unexpected --resume in args: %v", args)
	}
}

func TestBuildChatCompletionResponse_WithSessionID(t *testing.T) {
	data := buildChatCompletionResponse("test-model", "hello world", "session-123")

	var resp map[string]any
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if resp["object"] != "chat.completion" {
		t.Fatalf("object = %v, want chat.completion", resp["object"])
	}

	_, hasSessionID := resp["session_id"]
	if !hasSessionID {
		t.Error("response missing session_id field")
	}

	if resp["session_id"] != "session-123" {
		t.Errorf("session_id = %v, want session-123", resp["session_id"])
	}
}

func TestBuildChatCompletionResponse_WithoutSessionID(t *testing.T) {
	data := buildChatCompletionResponse("test-model", "hello world", "")

	var resp map[string]any
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if resp["object"] != "chat.completion" {
		t.Fatalf("object = %v, want chat.completion", resp["object"])
	}

	_, hasSessionID := resp["session_id"]
	if hasSessionID {
		t.Error("response should not have session_id field when empty")
	}
}

func TestBuildStreamRoleChunk_WithSessionID(t *testing.T) {
	data := buildStreamRoleChunk("chatcmpl-test", "test-model", "session-456")
	if len(data) == 0 {
		t.Fatal("empty chunk")
	}
	if string(data[:6]) != "data: " {
		t.Fatalf("prefix = %q, want 'data: '", string(data[:6]))
	}

	var chunk map[string]any
	if err := json.Unmarshal(data[6:], &chunk); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if chunk["object"] != "chat.completion.chunk" {
		t.Fatalf("object = %v, want chat.completion.chunk", chunk["object"])
	}

	_, hasSessionID := chunk["session_id"]
	if !hasSessionID {
		t.Error("chunk missing session_id field")
	}

	if chunk["session_id"] != "session-456" {
		t.Errorf("session_id = %v, want session-456", chunk["session_id"])
	}
}

func TestBuildStreamRoleChunk_WithoutSessionID(t *testing.T) {
	data := buildStreamRoleChunk("chatcmpl-test", "test-model", "")
	if len(data) == 0 {
		t.Fatal("empty chunk")
	}

	var chunk map[string]any
	if err := json.Unmarshal(data[6:], &chunk); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	_, hasSessionID := chunk["session_id"]
	if hasSessionID {
		t.Error("chunk should not have session_id field when empty")
	}
}
