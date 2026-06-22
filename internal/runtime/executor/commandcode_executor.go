package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// CommandCodeExecutor wraps the Command Code CLI (`cmd`) binary as a provider executor.
// It translates OpenAI-compatible chat requests into headless CLI subprocess calls and
// wraps the markdown output back into OpenAI-compatible JSON responses.
type CommandCodeExecutor struct {
	cfg *config.Config
}

// NewCommandCodeExecutor creates a CommandCodeExecutor bound to the given config.
func NewCommandCodeExecutor(cfg *config.Config) *CommandCodeExecutor {
	return &CommandCodeExecutor{cfg: cfg}
}

// Identifier implements cliproxyauth.ProviderExecutor.
func (e *CommandCodeExecutor) Identifier() string { return "commandcode" }

// resolveBinaryPath returns the path to the `cmd` binary, preferring config > PATH lookup.
func (e *CommandCodeExecutor) resolveBinaryPath() string {
	if e.cfg != nil {
		for _, key := range e.cfg.CommandCodeKey {
			if p := strings.TrimSpace(key.BinaryPath); p != "" {
				return p
			}
		}
	}
	if p, err := exec.LookPath("cmd"); err == nil {
		return p
	}
	return "cmd"
}

// resolveConfig returns the first CommandCodeKey config entry, or nil.
func (e *CommandCodeExecutor) resolveConfig() *config.CommandCodeKey {
	if e.cfg == nil {
		return nil
	}
	for i := range e.cfg.CommandCodeKey {
		return &e.cfg.CommandCodeKey[i]
	}
	return nil
}

// messagesToQuery converts an OpenAI messages array from the payload into a single text query.
func messagesToQuery(payload []byte) string {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return string(payload)
	}

	var parts []string
	messages.ForEach(func(_, msg gjson.Result) bool {
		role := strings.ToLower(strings.TrimSpace(msg.Get("role").String()))
		content := extractMessageContent(msg.Get("content"))
		if content == "" {
			return true
		}
		switch role {
		case "system":
			parts = append(parts, "System: "+content)
		case "user":
			parts = append(parts, content)
		case "assistant":
			parts = append(parts, "Assistant: "+content)
		}
		return true
	})

	return strings.Join(parts, "\n\n")
}

// extractMessageContent handles both string and array content formats from OpenAI messages.
func extractMessageContent(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if !content.IsArray() {
		return ""
	}
	var texts []string
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() == "text" {
			if text := strings.TrimSpace(part.Get("text").String()); text != "" {
				texts = append(texts, text)
			}
		}
		return true
	})
	return strings.Join(texts, "\n")
}

// buildArgs constructs the `cmd` CLI arguments from config and request.
func (e *CommandCodeExecutor) buildArgs(query string, model string, sessionID string) []string {
	args := []string{"-p", query}

	cfg := e.resolveConfig()

	useModel := strings.TrimSpace(model)
	if useModel == "" && cfg != nil {
		useModel = strings.TrimSpace(cfg.DefaultModel)
	}
	if useModel != "" {
		args = append(args, "-m", useModel)
	}

	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}

	if cfg != nil {
		if cfg.MaxTurns > 0 {
			args = append(args, "--max-turns", fmt.Sprintf("%d", cfg.MaxTurns))
		}
		if cfg.AutoAccept {
			args = append(args, "--auto-accept")
		}
		if cfg.Yolo {
			args = append(args, "--yolo")
		}
		if pm := strings.TrimSpace(cfg.PermissionMode); pm != "" {
			args = append(args, "--permission-mode", pm)
		}
	}

	return args
}

// extractSessionID extracts the session_id from the request payload.
func extractSessionID(payload []byte) string {
	sessionID := gjson.GetBytes(payload, "session_id").String()
	if strings.TrimSpace(sessionID) == "" {
		return ""
	}
	return strings.TrimSpace(sessionID)
}

// sessionSnapshot captures the state of the session directory before cmd runs.
type sessionSnapshot struct {
	entries map[string]time.Time
}

// snapshotSessionDir records all session files before cmd starts.
func snapshotSessionDir(projectDir string) (sessionSnapshot, error) {
	snap := sessionSnapshot{entries: make(map[string]time.Time)}
	sessionDir := filepath.Join(projectDir, ".commandcode", "projects")

	files, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return snap, nil
		}
		return snap, fmt.Errorf("read session dir: %w", err)
	}

	for _, f := range files {
		if f.IsDir() {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		snap.entries[f.Name()] = info.ModTime()
	}

	return snap, nil
}

// detectNewSession finds the session file created/modified after snapshot.
func detectNewSession(projectDir string, snap sessionSnapshot) (sessionID string, ok bool) {
	sessionDir := filepath.Join(projectDir, ".commandcode", "projects")

	files, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false
		}
		return "", false
	}

	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if !strings.HasSuffix(f.Name(), ".jsonl") {
			continue
		}

		info, err := f.Info()
		if err != nil {
			continue
		}

		modTime, exists := snap.entries[f.Name()]
		if !exists || info.ModTime().After(modTime) {
			return strings.TrimSuffix(f.Name(), ".jsonl"), true
		}
	}

	return "", false
}

// buildCommand creates the exec.Cmd for the CLI subprocess.
func (e *CommandCodeExecutor) buildCommand(ctx context.Context, query string, model string, sessionID string) *exec.Cmd {
	binPath := e.resolveBinaryPath()
	args := e.buildArgs(query, model, sessionID)

	cmd := exec.CommandContext(ctx, binPath, args...)

	cfg := e.resolveConfig()
	if cfg != nil && strings.TrimSpace(cfg.WorkingDir) != "" {
		cmd.Dir = strings.TrimSpace(cfg.WorkingDir)
	}

	cmd.Env = os.Environ()

	return cmd
}

// Execute implements cliproxyauth.ProviderExecutor.
func (e *CommandCodeExecutor) Execute(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
) (resp cliproxyexecutor.Response, err error) {
	baseModel := req.Model

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	query := messagesToQuery(req.Payload)
	if strings.TrimSpace(query) == "" {
		err = statusErr{code: http.StatusBadRequest, msg: "empty query"}
		return resp, err
	}

	sessionID := extractSessionID(req.Payload)
	var detectedSessionID string

	cfg := e.resolveConfig()
	workingDir := ""
	if cfg != nil {
		workingDir = strings.TrimSpace(cfg.WorkingDir)
	}

	if sessionID == "" && workingDir != "" {
		snap, snapErr := snapshotSessionDir(workingDir)
		if snapErr != nil {
			log.Debugf("commandcode executor: snapshot session dir: %v", snapErr)
		}
		defer func() {
			if detected, ok := detectNewSession(workingDir, snap); ok {
				detectedSessionID = detected
			}
		}()
	}

	helpCmd := helps.UpstreamRequestLog{
		URL:      e.resolveBinaryPath(),
		Method:   "exec",
		Body:     []byte(query),
		Provider: e.Identifier(),
	}
	if auth != nil {
		helpCmd.AuthID = auth.ID
		helpCmd.AuthLabel = auth.Label
	}
	helps.RecordAPIRequest(ctx, e.cfg, helpCmd)

	cmd := e.buildCommand(ctx, query, baseModel, sessionID)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	execErr := cmd.Run()
	elapsed := time.Since(start)

	if execErr != nil {
		stderrText := strings.TrimSpace(stderr.String())
		if stderrText == "" {
			stderrText = execErr.Error()
		}
		helps.RecordAPIResponseError(ctx, e.cfg, execErr)
		err = statusErr{code: http.StatusInternalServerError, msg: stderrText}
		return resp, err
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		output = "(no output)"
	}

	log.Debugf("commandcode executor: completed in %s, output length: %d", elapsed, len(output))

	responsePayload := buildChatCompletionResponse(baseModel, output, detectedSessionID)
	helps.AppendAPIResponseChunk(ctx, e.cfg, responsePayload)

	reporter.EnsurePublished(ctx)

	resp = cliproxyexecutor.Response{Payload: responsePayload}
	return resp, nil
}

// ExecuteStream implements cliproxyauth.ProviderExecutor.
func (e *CommandCodeExecutor) ExecuteStream(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := req.Model

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	query := messagesToQuery(req.Payload)
	if strings.TrimSpace(query) == "" {
		err = statusErr{code: http.StatusBadRequest, msg: "empty query"}
		return nil, err
	}

	sessionID := extractSessionID(req.Payload)
	var detectedSessionID string

	cfg := e.resolveConfig()
	workingDir := ""
	if cfg != nil {
		workingDir = strings.TrimSpace(cfg.WorkingDir)
	}

	if sessionID == "" && workingDir != "" {
		snap, snapErr := snapshotSessionDir(workingDir)
		if snapErr != nil {
			log.Debugf("commandcode executor: snapshot session dir: %v", snapErr)
		}
		defer func() {
			if detected, ok := detectNewSession(workingDir, snap); ok {
				detectedSessionID = detected
			}
		}()
	}

	helpCmd := helps.UpstreamRequestLog{
		URL:      e.resolveBinaryPath(),
		Method:   "exec",
		Body:     []byte(query),
		Provider: e.Identifier(),
	}
	if auth != nil {
		helpCmd.AuthID = auth.ID
		helpCmd.AuthLabel = auth.Label
	}
	helps.RecordAPIRequest(ctx, e.cfg, helpCmd)

	cmd := e.buildCommand(ctx, query, baseModel, sessionID)

	stdout, errPipe := cmd.StdoutPipe()
	if errPipe != nil {
		return nil, fmt.Errorf("commandcode executor: stdout pipe: %w", errPipe)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if errStart := cmd.Start(); errStart != nil {
		return nil, fmt.Errorf("commandcode executor: start: %w", errStart)
	}

	completionID := "chatcmpl-" + uuid.New().String()

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)

		initialChunk := buildStreamRoleChunk(completionID, baseModel, detectedSessionID)
		select {
		case out <- cliproxyexecutor.StreamChunk{Payload: initialChunk}:
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return
		}

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(nil, 52_428_800)

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			chunk := buildStreamContentChunk(completionID, baseModel, line)
			helps.AppendAPIResponseChunk(ctx, e.cfg, chunk)
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
			case <-ctx.Done():
				_ = cmd.Process.Kill()
				return
			}
		}

		waitErr := cmd.Wait()
		if waitErr != nil {
			stderrText := strings.TrimSpace(stderr.String())
			if stderrText == "" {
				stderrText = waitErr.Error()
			}
			helps.RecordAPIResponseError(ctx, e.cfg, waitErr)
			errStream := statusErr{code: http.StatusInternalServerError, msg: stderrText}
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errStream}:
			case <-ctx.Done():
			}
			return
		}

		finishChunk := buildStreamFinishChunk(completionID, baseModel)
		helps.AppendAPIResponseChunk(ctx, e.cfg, finishChunk)
		select {
		case out <- cliproxyexecutor.StreamChunk{Payload: finishChunk}:
		case <-ctx.Done():
			return
		}

		reporter.EnsurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{Chunks: out}, nil
}

// Refresh implements cliproxyauth.ProviderExecutor. No-op since the CLI manages its own auth.
func (e *CommandCodeExecutor) Refresh(
	_ context.Context,
	auth *cliproxyauth.Auth,
) (*cliproxyauth.Auth, error) {
	return auth, nil
}

// CountTokens implements cliproxyauth.ProviderExecutor. Returns zero since the CLI does not report usage.
func (e *CommandCodeExecutor) CountTokens(
	_ context.Context,
	_ *cliproxyauth.Auth,
	_ cliproxyexecutor.Request,
	_ cliproxyexecutor.Options,
) (cliproxyexecutor.Response, error) {
	usageJSON := []byte(`{"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
	return cliproxyexecutor.Response{Payload: usageJSON}, nil
}

// HttpRequest implements cliproxyauth.ProviderExecutor. Not supported for CLI executors.
func (e *CommandCodeExecutor) HttpRequest(
	_ context.Context,
	_ *cliproxyauth.Auth,
	_ *http.Request,
) (*http.Response, error) {
	return nil, fmt.Errorf("commandcode executor: HttpRequest not supported")
}

// buildChatCompletionResponse wraps CLI markdown output in OpenAI chat.completion JSON.
func buildChatCompletionResponse(model, content, sessionID string) []byte {
	id := "chatcmpl-" + uuid.New().String()
	created := time.Now().Unix()

	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		},
	}

	if sessionID != "" {
		resp["session_id"] = sessionID
	}

	data, _ := json.Marshal(resp)
	return data
}

// buildStreamRoleChunk creates the initial SSE chunk with the assistant role delta.
func buildStreamRoleChunk(id, model, sessionID string) []byte {
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         map[string]any{"role": "assistant"},
				"finish_reason": nil,
			},
		},
	}

	if sessionID != "" {
		chunk["session_id"] = sessionID
	}

	data, _ := json.Marshal(chunk)
	return append([]byte("data: "), data...)
}

// buildStreamContentChunk creates an SSE chunk with content delta.
func buildStreamContentChunk(id, model, content string) []byte {
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         map[string]any{"content": content},
				"finish_reason": nil,
			},
		},
	}
	data, _ := json.Marshal(chunk)
	return append([]byte("data: "), data...)
}

// buildStreamFinishChunk creates the final SSE chunk with finish_reason stop.
func buildStreamFinishChunk(id, model string) []byte {
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "stop",
			},
		},
	}
	data, _ := json.Marshal(chunk)
	return append([]byte("data: "), data...)
}
