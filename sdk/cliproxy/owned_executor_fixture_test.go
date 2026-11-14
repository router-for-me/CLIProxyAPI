package cliproxy

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const ownedTestProvider = "external-provider"

type ownedTestExecutor struct {
	provider                 string
	mu                       sync.Mutex
	calls, refreshes, closes int
	seenAuth                 bool
	unauthorized             bool
	entered, release         chan struct{}
	enterOnce                sync.Once
}

func (e *ownedTestExecutor) Identifier() string { return e.provider }

func (e *ownedTestExecutor) Execute(ctx context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.calls++
	e.seenAuth = auth != nil && auth.ID == "owned-fixture-auth" && auth.Provider == e.provider
	unauthorized := e.unauthorized && auth.Metadata["access_token"] != "synthetic-rotated"
	e.mu.Unlock()
	if e.entered != nil {
		e.enterOnce.Do(func() { close(e.entered) })
		select {
		case <-e.release:
		case <-ctx.Done():
			return coreexecutor.Response{}, ctx.Err()
		}
	}
	if unauthorized {
		return coreexecutor.Response{}, ownedUnauthorized{}
	}
	return coreexecutor.Response{Payload: []byte("owned-result")}, nil
}

func (e *ownedTestExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	response, errExecute := e.Execute(ctx, auth, req, opts)
	if errExecute != nil {
		return nil, errExecute
	}
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: response.Payload}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *ownedTestExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (e *ownedTestExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	e.mu.Lock()
	e.refreshes++
	e.mu.Unlock()
	updated := auth.Clone()
	updated.Metadata["access_token"] = "synthetic-rotated"
	updated.Metadata["expired"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	return updated, nil
}

func (e *ownedTestExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
}

func (e *ownedTestExecutor) RequestToFormat(coreexecutor.Request, coreexecutor.Options) sdktranslator.Format {
	return sdktranslator.Format("owned-format")
}

func (e *ownedTestExecutor) CloseExecutionSession(string) {
	e.mu.Lock()
	e.closes++
	e.mu.Unlock()
}

func (e *ownedTestExecutor) counts() (int, int, int, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls, e.refreshes, e.closes, e.seenAuth
}

type ownedUnauthorized struct{}

func (ownedUnauthorized) Error() string   { return "synthetic unauthorized" }
func (ownedUnauthorized) StatusCode() int { return http.StatusUnauthorized }

type ownedNonComparable struct {
	serviceTestPluginExecutor
	values []int
}

func (ownedNonComparable) Identifier() string { return ownedTestProvider }

type ownedNestedInterface struct {
	serviceTestPluginExecutor
	value any
}

func (ownedNestedInterface) Identifier() string { return ownedTestProvider }

type ownedNonReflexive struct {
	serviceTestPluginExecutor
	value  float64
	closes *atomic.Int32
}

func (ownedNonReflexive) Identifier() string             { return ownedTestProvider }
func (e ownedNonReflexive) CloseExecutionSession(string) { e.closes.Add(1) }

type ownedChannel chan struct{}

func (ownedChannel) Identifier() string { return ownedTestProvider }
func (ownedChannel) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}
func (ownedChannel) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, nil
}
func (ownedChannel) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}
func (ownedChannel) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}
func (ownedChannel) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func ownedTestBuilder(t *testing.T, manager *coreauth.Manager) *Builder {
	t.Helper()
	return NewBuilder().WithConfig(&config.Config{}).WithConfigPath(t.TempDir() + "/config.yaml").WithCoreAuthManager(manager)
}

func ownedTestService(t *testing.T, owner *ownedTestExecutor) (*Service, *coreauth.Manager, *coreauth.Auth) {
	t.Helper()
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	service, errBuild := ownedTestBuilder(t, manager).WithOwnedProviderExecutor(owner.provider, owner).Build()
	if errBuild != nil {
		t.Fatal("owned builder rejected valid registration")
	}
	t.Cleanup(func() {
		if errShutdown := service.Shutdown(context.Background()); errShutdown != nil {
			t.Error("owned service cleanup failed")
		}
	})
	auth := &coreauth.Auth{ID: "owned-fixture-auth", Provider: owner.provider, Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "synthetic-initial", "refresh_token": "synthetic-refresh"}}
	registered, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), auth)
	if errRegister != nil {
		t.Fatal("owned auth registration failed")
	}
	GlobalModelRegistry().RegisterClient(auth.ID, auth.Provider, []*ModelInfo{{ID: "owned-model", Object: "model", OwnedBy: auth.Provider}})
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	return service, manager, registered
}

func ownedTestCall(ctx context.Context, manager *coreauth.Manager, mode string) error {
	req := coreexecutor.Request{Model: "owned-model"}
	opts := coreexecutor.Options{Metadata: map[string]any{coreexecutor.PinnedAuthMetadataKey: "owned-fixture-auth"}}
	switch mode {
	case "count":
		response, errCall := manager.ExecuteCount(ctx, []string{ownedTestProvider}, req, opts)
		if errCall != nil {
			return errCall
		}
		if string(response.Payload) != "owned-result" {
			return context.Canceled
		}
	case "stream":
		stream, errCall := manager.ExecuteStream(ctx, []string{ownedTestProvider}, req, opts)
		if errCall != nil {
			return errCall
		}
		seen := false
		for chunk := range stream.Chunks {
			if chunk.Err != nil {
				return chunk.Err
			}
			seen = seen || string(chunk.Payload) == "owned-result"
		}
		if !seen {
			return context.Canceled
		}
	default:
		response, errCall := manager.Execute(ctx, []string{ownedTestProvider}, req, opts)
		if errCall != nil {
			return errCall
		}
		if string(response.Payload) != "owned-result" {
			return context.Canceled
		}
	}
	return nil
}
