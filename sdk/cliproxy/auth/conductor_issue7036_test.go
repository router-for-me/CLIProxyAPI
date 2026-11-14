package auth

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// issue7036StatusErr replays the terminal error the Codex executors build for
// the Daybreak hardware-key reauthentication cyber_policy rejection (issue
// #7036): a 400 passthrough body scoped to the credential's OAuth session.
type issue7036StatusErr struct {
	body             string
	credentialScoped bool
}

func (e issue7036StatusErr) Error() string            { return e.body }
func (e issue7036StatusErr) StatusCode() int          { return http.StatusBadRequest }
func (e issue7036StatusErr) IsCredentialScoped() bool { return e.credentialScoped }

const issue7036HardwareKeyBody = `{"error":{"type":"invalid_request","code":"cyber_policy","message":"Your session must be reauthenticated with an approved physical hardware key to continue using Codex.","param":null}}`

const issue7036ModerationBody = `{"error":{"type":"invalid_request","code":"cyber_policy","message":"This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request. To get authorized for security work, join the Trusted Access for Cyber program: https://chatgpt.com/cyber","param":null}}`

type issue7036Executor struct {
	mu           sync.Mutex
	id           string
	failingAuths map[string]string
	executeCalls []string
	streamCalls  []string
}

func (e *issue7036Executor) Identifier() string { return e.id }

func (e *issue7036Executor) failureFor(authID string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	body, failing := e.failingAuths[authID]
	return body, failing
}

func (e *issue7036Executor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeCalls = append(e.executeCalls, auth.ID)
	e.mu.Unlock()
	if body, failing := e.failureFor(auth.ID); failing {
		return cliproxyexecutor.Response{}, issue7036StatusErr{body: body, credentialScoped: true}
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID + ":ok")}, nil
}

func (e *issue7036Executor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, auth.ID)
	e.mu.Unlock()
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	if body, failing := e.failureFor(auth.ID); failing {
		// The terminal rejection arrives before any output chunk, so the
		// conductor observes it during stream bootstrap and can fail over.
		ch <- cliproxyexecutor.StreamChunk{Err: issue7036StatusErr{body: body, credentialScoped: true}}
		close(ch)
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID + ":ok")}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *issue7036Executor) Refresh(_ context.Context, _ *Auth) (*Auth, error) {
	return nil, nil
}

func (e *issue7036Executor) CountTokens(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *issue7036Executor) HttpRequest(_ context.Context, _ *Auth, req *http.Request) (*http.Response, error) {
	return nil, context.Canceled
}

func newIssue7036Manager(t *testing.T, model string, executor *issue7036Executor, auths ...*Auth) *Manager {
	t.Helper()
	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.RegisterExecutor(executor)
	reg := registry.GetGlobalRegistry()
	for _, auth := range auths {
		reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() {
			reg.UnregisterClient(auth.ID)
		})
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}
	return m
}

func newIssue7036CodexAuth(id string) *Auth {
	now := time.Now()
	return &Auth{
		ID:       id,
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  id + "-access",
			"refresh_token": id + "-refresh",
			"expired":       now.Add(24 * time.Hour).Format(time.RFC3339),
		},
	}
}

// TestIssue7036_HardwareKeyReauthRotatesAndCoolsCredential replays the Daybreak
// hardware-key cyber_policy terminal error on the first credential: the request
// must fail over to the healthy credential and the dead session must be cooled
// down with a credential-bound status message.
func TestIssue7036_HardwareKeyReauthRotatesAndCoolsCredential(t *testing.T) {
	model := "gpt-5.6-luna"
	primary := newIssue7036CodexAuth("aa-codex-7036-dead")
	backup := newIssue7036CodexAuth("bb-codex-7036-healthy")
	executor := &issue7036Executor{
		id:           "codex",
		failingAuths: map[string]string{primary.ID: issue7036HardwareKeyBody},
	}
	m := newIssue7036Manager(t, model, executor, primary, backup)

	resp, errExec := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExec != nil {
		t.Fatalf("Execute error = %v, want failover to the healthy credential", errExec)
	}
	if got := string(resp.Payload); got != backup.ID+":ok" {
		t.Fatalf("Execute payload = %q, want backup %q", got, backup.ID+":ok")
	}

	dead, ok := m.GetByID(primary.ID)
	if !ok || dead == nil {
		t.Fatal("primary auth missing after hardware-key failure")
	}
	if !dead.Unavailable || dead.Status != StatusError {
		t.Fatalf("dead credential state: Unavailable=%v Status=%s, want true/StatusError", dead.Unavailable, dead.Status)
	}
	if !strings.Contains(strings.ToLower(dead.StatusMessage), "hardware key") {
		t.Fatalf("StatusMessage = %q, want the upstream hardware-key reauthentication text", dead.StatusMessage)
	}
	if remaining := time.Until(dead.NextRetryAfter); remaining < 29*time.Minute {
		t.Fatalf("NextRetryAfter = %v (%v remaining), want the 30-minute credential cooldown", dead.NextRetryAfter, remaining)
	}
	state := dead.ModelStates[model]
	if state == nil || !state.Unavailable {
		t.Fatalf("model state after hardware-key failure = %+v, want unavailable", state)
	}

	// The cooled credential must not be picked again while the cooldown holds.
	executor.mu.Lock()
	executor.executeCalls = nil
	executor.mu.Unlock()
	respSecond, errSecond := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errSecond != nil {
		t.Fatalf("second Execute error = %v, want success via backup", errSecond)
	}
	if got := string(respSecond.Payload); got != backup.ID+":ok" {
		t.Fatalf("second Execute payload = %q, want backup", got)
	}
	executor.mu.Lock()
	calls := append([]string(nil), executor.executeCalls...)
	executor.mu.Unlock()
	for _, id := range calls {
		if id == primary.ID {
			t.Fatal("cooled hardware-key credential was selected again before its cooldown expired")
		}
	}
}

// TestIssue7036_HardwareKeyReauthStreamFailsOver replays the same rejection on
// the streaming path: the terminal frame arrives during bootstrap, so the
// conductor must fail over instead of surfacing the error after committing.
func TestIssue7036_HardwareKeyReauthStreamFailsOver(t *testing.T) {
	model := "gpt-5.6-luna"
	primary := newIssue7036CodexAuth("aa-codex-7036-stream-dead")
	backup := newIssue7036CodexAuth("bb-codex-7036-stream-healthy")
	executor := &issue7036Executor{
		id:           "codex",
		failingAuths: map[string]string{primary.ID: issue7036HardwareKeyBody},
	}
	m := newIssue7036Manager(t, model, executor, primary, backup)

	stream, errStream := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errStream != nil {
		t.Fatalf("ExecuteStream error = %v, want failover to the healthy credential", errStream)
	}
	var payload string
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error after failover = %v, want backup success", chunk.Err)
		}
		if len(chunk.Payload) > 0 {
			payload = string(chunk.Payload)
		}
	}
	if payload != backup.ID+":ok" {
		t.Fatalf("stream payload = %q, want backup %q", payload, backup.ID+":ok")
	}

	dead, ok := m.GetByID(primary.ID)
	if !ok || dead == nil {
		t.Fatal("primary auth missing after stream hardware-key failure")
	}
	if !dead.Unavailable || dead.Status != StatusError {
		t.Fatalf("dead credential state after stream failure: Unavailable=%v Status=%s, want true/StatusError", dead.Unavailable, dead.Status)
	}
}

// TestIssue7036_ModerationCyberPolicyStaysRequestFault keeps the existing
// behavior for the content-moderation cyber_policy variant: the terminal
// upstream error is preserved for the client and no credential state is
// recorded.
func TestIssue7036_ModerationCyberPolicyStaysRequestFault(t *testing.T) {
	model := "gpt-5.6-luna"
	primary := newIssue7036CodexAuth("aa-codex-7036-moderation")
	executor := &issue7036Executor{
		id:           "codex",
		failingAuths: map[string]string{primary.ID: issue7036ModerationBody},
	}
	m := newIssue7036Manager(t, model, executor, primary)

	_, errExec := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("expected the content-moderation cyber_policy error to surface")
	}
	if !strings.Contains(errExec.Error(), "cyber_policy") {
		t.Fatalf("error lost the terminal upstream body: %v", errExec)
	}
	if statusCodeFromError(errExec) != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 passthrough", statusCodeFromError(errExec))
	}

	after, ok := m.GetByID(primary.ID)
	if !ok || after == nil {
		t.Fatal("auth missing after moderation rejection")
	}
	if after.Unavailable {
		t.Fatalf("content-moderation rejection cooled the credential: StatusMessage=%q", after.StatusMessage)
	}
}
