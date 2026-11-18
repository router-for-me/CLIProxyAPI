package auth

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Both credentials start cooling down, so these public Manager calls reach the
// credits path without an ordinary executor call first authorizing the request.
func TestAuthExecutionPolicyAntigravityCreditsFallback(t *testing.T) {
	for _, kind := range []AuthUseKind{AuthUseExecute, AuthUseStream} {
		for _, mode := range []string{"deny_prepare", "deny_dispatch", "allow", "nil_policy"} {
			t.Run(string(kind)+"/"+mode, func(t *testing.T) {
				f := newCreditsPolicyFixture(t)
				var checked []AuthUse
				if mode != "nil_policy" {
					f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(ctx context.Context, use AuthUse) error {
						checked = append(checked, use)
						if !AntigravityCreditsRequested(ctx) || use.Auth == nil || use.Auth.Provider != "antigravity" {
							t.Error("credits policy actual input mismatch")
						}
						if use.Kind == AuthUsePrepare {
							if use.RequestedModel != "" || use.Model != "" {
								t.Error("credits policy prepare model mismatch")
							}
						} else if use.Kind != kind || use.RequestedModel != "public-credits-model" || use.Model != f.model {
							t.Error("credits policy dispatch model mismatch")
						}
						if mode == "deny_prepare" && use.Kind == AuthUsePrepare || mode == "deny_dispatch" && use.Kind == kind {
							return errors.New("private policy canary")
						}
						return nil
					}))
				}
				err := f.invoke(kind)
				denied := strings.HasPrefix(mode, "deny_")
				if denied {
					// A denial applies to every matching use, including any alternate.
					// The call trace additionally proves that B was never reconsidered.
					if !isRequestStopError(err) || statusCodeFromError(err) != 503 || err.Error() != "selected account execution is unavailable" {
						t.Error("credits policy refusal changed")
					}
					f.requireUnchangedHealth(t)
				} else if err != nil {
					t.Error("credits policy allowed request failed")
				}
				wantChecks, wantPrepare, wantDispatch := 2, 1, 1
				switch mode {
				case "deny_prepare":
					wantChecks, wantPrepare, wantDispatch = 1, 0, 0
				case "deny_dispatch":
					wantDispatch = 0
				case "nil_policy":
					wantChecks = 0
				}
				if len(checked) != wantChecks {
					t.Error("credits policy check count changed")
				}
				for _, use := range checked {
					if use.Auth == nil || use.Auth.ID != f.auths[0].ID || use.Auth.RegistrationEpoch != f.auths[0].RegistrationEpoch ||
						use.Auth.CredentialVersion != f.auths[0].CredentialVersion || !reflect.DeepEqual(use.Auth.Metadata, f.auths[0].Metadata) {
						t.Error("credits policy selected credential changed")
					}
				}
				f.exec.mu.Lock()
				defer f.exec.mu.Unlock()
				if len(f.exec.prepared) != wantPrepare || len(f.exec.dispatched) != wantDispatch || f.exec.nonCredits != 0 {
					t.Error("credits policy provider dispatch changed")
				}
				for _, id := range append(append([]string(nil), f.exec.prepared...), f.exec.dispatched...) {
					if id != f.auths[0].ID {
						t.Error("credits policy alternate credential attempted")
					}
				}
			})
		}
	}
}

type creditsPolicyExecutor struct {
	authUseExecutor
	prepared   []string
	dispatched []string
	nonCredits int
}

func (e *creditsPolicyExecutor) ShouldPrepareRequestAuth(*Auth) bool { return true }
func (e *creditsPolicyExecutor) PrepareRequestAuth(ctx context.Context, a *Auth) (*Auth, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.prepared = append(e.prepared, a.ID)
	if !AntigravityCreditsRequested(ctx) {
		e.nonCredits++
	}
	// No changed credential is returned, so this control does not exercise a
	// transition observer or alter the existing cooldown during preparation.
	return nil, nil
}
func (e *creditsPolicyExecutor) Execute(ctx context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.recordDispatch(ctx, a)
	return cliproxyexecutor.Response{Payload: []byte("credits policy response")}, nil
}
func (e *creditsPolicyExecutor) ExecuteStream(ctx context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.recordDispatch(ctx, a)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("credits policy response")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}
func (e *creditsPolicyExecutor) recordDispatch(ctx context.Context, a *Auth) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.dispatched = append(e.dispatched, a.ID)
	if !AntigravityCreditsRequested(ctx) {
		e.nonCredits++
	}
}

type creditsPolicyFixture struct {
	manager *Manager
	exec    *creditsPolicyExecutor
	hook    *authUseResultHook
	auths   []*Auth
	ctx     context.Context
	model   string
}

func newCreditsPolicyFixture(t *testing.T) *creditsPolicyFixture {
	t.Helper()
	f := &creditsPolicyFixture{model: "claude-sonnet-4-6", hook: &authUseResultHook{}}
	var cancel context.CancelFunc
	f.ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	f.manager = NewManager(nil, nil, f.hook)
	f.manager.SetConfig(&internalconfig.Config{QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true}})
	f.manager.SetRetryConfig(0, 0, 1)
	f.exec = &creditsPolicyExecutor{authUseExecutor: authUseExecutor{provider: "antigravity"}}
	f.manager.RegisterExecutor(f.exec)
	for _, suffix := range []string{"a", "b"} {
		id := "credits-policy-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + suffix
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: f.model}})
		t.Cleanup(func() {
			reg.UnregisterClient(id)
			antigravityCreditsHintByAuth.Delete(id)
		})
		until := time.Now().Add(30 * time.Minute)
		a, err := f.manager.Register(f.ctx, &Auth{ID: id, Provider: "antigravity", Status: StatusActive,
			Metadata: map[string]any{"access_token": "synthetic-" + suffix},
			ModelStates: map[string]*ModelState{f.model: {Unavailable: true, NextRetryAfter: until,
				Quota: QuotaState{Exceeded: true, NextRecoverAt: until}}},
		})
		if err != nil || a == nil {
			t.Fatal("credits policy registration failed")
		}
		SetAntigravityCreditsHint(id, AntigravityCreditsHint{Known: true, Available: true, UpdatedAt: time.Now()})
		f.auths = append(f.auths, a.Clone())
	}
	return f
}

func (f *creditsPolicyFixture) invoke(kind AuthUseKind) error {
	req := cliproxyexecutor.Request{Model: f.model}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "public-credits-model"}}
	if kind == AuthUseExecute {
		response, err := f.manager.Execute(f.ctx, []string{"antigravity"}, req, opts)
		if err == nil && string(response.Payload) != "credits policy response" {
			return errors.New("credits policy response mismatch")
		}
		return err
	}
	opts.Stream = true
	result, err := f.manager.ExecuteStream(f.ctx, []string{"antigravity"}, req, opts)
	if err != nil {
		return err
	}
	if result == nil || result.Chunks == nil {
		return errors.New("credits policy stream missing")
	}
	var payload []byte
	var streamErr error
	for {
		select {
		case chunk, open := <-result.Chunks:
			if !open {
				if streamErr != nil {
					return streamErr
				}
				if string(payload) != "credits policy response" {
					return errors.New("credits policy response mismatch")
				}
				return nil
			}
			payload = append(payload, chunk.Payload...)
			if chunk.Err != nil {
				streamErr = chunk.Err
			}
		case <-f.ctx.Done():
			return f.ctx.Err()
		}
	}
}

func (f *creditsPolicyFixture) requireUnchangedHealth(t *testing.T) {
	t.Helper()
	if f.hook.results.Load() != 0 {
		t.Error("credits policy refusal recorded health result")
	}
	for _, before := range f.auths {
		after, ok := f.manager.GetByID(before.ID)
		if !ok || after.Failed != before.Failed || after.Unavailable != before.Unavailable ||
			!reflect.DeepEqual(after.LastError, before.LastError) || !reflect.DeepEqual(after.ModelStates, before.ModelStates) ||
			!reflect.DeepEqual(after.Quota, before.Quota) || !after.NextRetryAfter.Equal(before.NextRetryAfter) {
			t.Error("credits policy refusal changed health")
		}
	}
}
