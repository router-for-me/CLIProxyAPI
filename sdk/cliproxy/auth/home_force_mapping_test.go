package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalhome "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestHomeForceMappingAliasResult(t *testing.T) {
	auth := &Auth{
		Provider: "xai",
		Attributes: map[string]string{
			homeUpstreamModelAttributeKey: "grok-4.5",
			homeForceMappingAttributeKey:  "true",
			homeOriginalAliasAttributeKey: "grok-latest",
		},
	}

	result := homeForceMappingAliasResult(auth, "grok-latest")
	if result.UpstreamModel != "grok-4.5" || !result.ForceMapping || result.OriginalAlias != "grok-latest" {
		t.Fatalf("homeForceMappingAliasResult() = %+v", result)
	}
}

func TestHomeForceMappingAliasResultRequiresSameOriginalAlias(t *testing.T) {
	auth := &Auth{
		Provider: "xai",
		Attributes: map[string]string{
			homeUpstreamModelAttributeKey: "grok-4.5",
			homeForceMappingAttributeKey:  "true",
			homeOriginalAliasAttributeKey: "grok-latest",
		},
	}

	if result := homeForceMappingAliasResult(auth, " GROK-LATEST "); !result.ForceMapping {
		t.Fatalf("homeForceMappingAliasResult() = %+v, want same alias force mapping", result)
	}
	if result := homeForceMappingAliasResult(auth, "grok-latest(high)"); !result.ForceMapping {
		t.Fatalf("homeForceMappingAliasResult() = %+v, want reasoning suffix force mapping", result)
	}
	if result := homeForceMappingAliasResult(auth, "grok-latest(custom)"); result.ForceMapping || result.OriginalAlias != "" {
		t.Fatalf("homeForceMappingAliasResult() = %+v, want no force mapping for a custom suffix", result)
	}
	if result := homeForceMappingAliasResult(auth, "grok-other"); result.ForceMapping || result.OriginalAlias != "" {
		t.Fatalf("homeForceMappingAliasResult() = %+v, want no force mapping for a different alias", result)
	}
}

func TestHomeNonForceAliasSessionReuseAndTargetChangeReleasesAccountedModel(t *testing.T) {
	registry := executionregistry.New()
	dispatcher := &accountedAliasTargetDispatcher{}
	var releases []executionregistry.ReleaseGroup
	var releasesMu sync.Mutex
	registry.SetReleaseSink(func(group executionregistry.ReleaseGroup, _ int64) {
		releasesMu.Lock()
		releases = append(releases, group)
		releasesMu.Unlock()
		dispatcher.releases.Add(1)
	})

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, registry, 1)
	manager.RegisterExecutor(forceMappingAliasChangeExecutor{})
	t.Cleanup(func() { manager.CloseExecutionSession("non-force-alias-session") })

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "non-force-alias-session",
		cliproxyexecutor.PinnedAuthMetadataKey:       "non-force-alias-auth",
	}}
	for _, model := range []string{"alias-a(high)", "alias-a", "alias-b"} {
		if _, errExecute := manager.Execute(ctx, []string{"force-mapping"}, cliproxyexecutor.Request{Model: model}, opts); errExecute != nil {
			t.Fatalf("Execute(%q) error = %v", model, errExecute)
		}
	}
	if got := dispatcher.calls.Load(); got != 2 {
		t.Fatalf("Home RPOP calls = %d, want 2 for same-route reuse and target change", got)
	}
	if !dispatcher.releasedBeforeSecondRPop.Load() {
		t.Fatal("previous accounted selection was not released before the different-alias redispatch")
	}

	manager.CloseExecutionSession("non-force-alias-session")
	releasesMu.Lock()
	gotReleases := append([]executionregistry.ReleaseGroup(nil), releases...)
	releasesMu.Unlock()
	wantReleases := []executionregistry.ReleaseGroup{
		{CredentialID: "non-force-alias-auth", Model: "target-a"},
		{CredentialID: "non-force-alias-auth", Model: "target-b"},
	}
	if !reflect.DeepEqual(gotReleases, wantReleases) {
		t.Fatalf("accounted release groups = %#v, want %#v", gotReleases, wantReleases)
	}
}

type accountedAliasTargetDispatcher struct {
	calls                    atomic.Int32
	releases                 atomic.Int32
	releasedBeforeSecondRPop atomic.Bool
}

func (*accountedAliasTargetDispatcher) HeartbeatOK() bool { return true }

func (d *accountedAliasTargetDispatcher) RPopAuth(_ context.Context, model string, _ string, _ http.Header, _ int) ([]byte, error) {
	call := d.calls.Add(1)
	if call == 2 {
		d.releasedBeforeSecondRPop.Store(d.releases.Load() == 1)
	}
	target := "target-a"
	if canonicalHomeConcurrencyModelKey(model) == "alias-b" {
		target = "target-b"
	}
	return json.Marshal(map[string]any{
		"model":      target,
		"auth_index": "non-force-alias-auth",
		"auth": Auth{
			ID:       "non-force-alias-auth",
			Provider: "force-mapping",
			Status:   StatusActive,
			Attributes: map[string]string{
				"websockets": "true",
			},
		},
		"concurrency": homeConcurrencyTuple{
			Accounted:    true,
			CredentialID: "non-force-alias-auth",
			Model:        target,
		},
	})
}

func (*accountedAliasTargetDispatcher) AbortAmbiguousDispatch() {}

func TestHomeAuthSelectionRouteRetainsRequestedResponseAliasAcrossWebsocketReuse(t *testing.T) {
	registry := executionregistry.New()
	dispatcher := &authSelectionAliasDispatcher{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, registry, 1)
	manager.RegisterExecutor(authSelectionAliasExecutor{})
	t.Cleanup(func() { manager.CloseExecutionSession("auth-selection-route") })

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.AuthSelectionModelMetadataKey: "route-model",
		cliproxyexecutor.RequestedModelMetadataKey:     "client-alias",
		cliproxyexecutor.ExecutionSessionMetadataKey:   "auth-selection-route",
		cliproxyexecutor.PinnedAuthMetadataKey:         "auth-selection-route-auth",
	}}
	for attempt := 0; attempt < 2; attempt++ {
		response, errExecute := manager.Execute(ctx, []string{"force-mapping"}, cliproxyexecutor.Request{Model: "execution-model"}, opts)
		if errExecute != nil {
			t.Fatalf("Execute() error = %v", errExecute)
		}
		if got := string(response.Payload); got != `{"model":"client-alias"}` {
			t.Fatalf("response = %s, want requested response alias", got)
		}
	}
	if got := dispatcher.Models(); !reflect.DeepEqual(got, []string{"route-model"}) {
		t.Fatalf("Home RPOP models = %#v, want canonical auth-selection route", got)
	}
}

type authSelectionAliasDispatcher struct {
	mu     sync.Mutex
	models []string
}

func (*authSelectionAliasDispatcher) HeartbeatOK() bool { return true }

func (d *authSelectionAliasDispatcher) RPopAuth(_ context.Context, model string, _ string, _ http.Header, _ int) ([]byte, error) {
	d.mu.Lock()
	d.models = append(d.models, model)
	d.mu.Unlock()
	return json.Marshal(map[string]any{
		"model":          "target-model",
		"force_mapping":  true,
		"original_alias": "route-model",
		"auth_index":     "auth-selection-route-auth",
		"auth": Auth{
			ID:       "auth-selection-route-auth",
			Provider: "force-mapping",
			Status:   StatusActive,
			Attributes: map[string]string{
				"websockets": "true",
			},
		},
		"concurrency": homeConcurrencyTuple{Accounted: true, CredentialID: "auth-selection-route-auth", Model: "target-model"},
	})
}

func (*authSelectionAliasDispatcher) AbortAmbiguousDispatch() {}

func (d *authSelectionAliasDispatcher) Models() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.models...)
}

type authSelectionAliasExecutor struct{}

func (authSelectionAliasExecutor) Identifier() string { return "force-mapping" }
func (authSelectionAliasExecutor) Execute(_ context.Context, _ *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if lifecycle, ok := opts.ExecutionLifecycle.(interface{ Retain() }); ok {
		lifecycle.Retain()
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"model":"` + req.Model + `"}`)}, nil
}
func (authSelectionAliasExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (authSelectionAliasExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (authSelectionAliasExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (authSelectionAliasExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestHomeForceMappingAliasChangeEndsAndFlushesBeforeRedispatch(t *testing.T) {
	registry := executionregistry.New()
	dispatcher := &forceMappingAliasChangeDispatcher{}
	registry.SetReleaseSink(func(executionregistry.ReleaseGroup, int64) {
		dispatcher.releases.Add(1)
	})

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, registry, 1)
	manager.RegisterExecutor(forceMappingAliasChangeExecutor{})
	t.Cleanup(func() { manager.CloseExecutionSession("force-mapping-alias-change") })

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "force-mapping-alias-change",
		cliproxyexecutor.PinnedAuthMetadataKey:       "force-mapping-auth",
	}}
	for _, model := range []string{"alias-a", "alias-b"} {
		if _, errExecute := manager.Execute(ctx, []string{"force-mapping"}, cliproxyexecutor.Request{Model: model}, opts); errExecute != nil {
			t.Fatalf("Execute(%q) error = %v", model, errExecute)
		}
	}
	if got := dispatcher.calls.Load(); got != 2 {
		t.Fatalf("Home RPOP calls = %d, want 2 after original alias changes", got)
	}
	if !dispatcher.releasedBeforeSecondRPop.Load() {
		t.Fatal("previous selection was not ended and released before the second Home RPOP")
	}
}

type forceMappingAliasChangeDispatcher struct {
	calls                    atomic.Int32
	releases                 atomic.Int32
	releasedBeforeSecondRPop atomic.Bool
}

func (*forceMappingAliasChangeDispatcher) HeartbeatOK() bool { return true }

func (d *forceMappingAliasChangeDispatcher) RPopAuth(_ context.Context, _ string, _ string, _ http.Header, _ int) ([]byte, error) {
	if d.calls.Add(1) == 2 {
		d.releasedBeforeSecondRPop.Store(d.releases.Load() == 1)
	}
	return json.Marshal(map[string]any{
		"model":      "upstream-a",
		"auth_index": "force-mapping-auth",
		"auth": Auth{
			ID:       "force-mapping-auth",
			Provider: "force-mapping",
			Status:   StatusActive,
			Attributes: map[string]string{
				"websockets":                  "true",
				homeForceMappingAttributeKey:  "true",
				homeOriginalAliasAttributeKey: "alias-a",
			},
		},
		"concurrency": homeConcurrencyTuple{
			Accounted:    true,
			CredentialID: "force-mapping-auth",
			Model:        "upstream-a",
		},
	})
}

func (*forceMappingAliasChangeDispatcher) AbortAmbiguousDispatch() {}

type forceMappingAliasChangeExecutor struct{}

func (forceMappingAliasChangeExecutor) Identifier() string { return "force-mapping" }
func (forceMappingAliasChangeExecutor) Execute(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if lifecycle, ok := opts.ExecutionLifecycle.(interface{ Retain() }); ok {
		lifecycle.Retain()
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}
func (forceMappingAliasChangeExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (forceMappingAliasChangeExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (forceMappingAliasChangeExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (forceMappingAliasChangeExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestHomeRetainedRouteRewritesReasoningSuffixAndWaitsForReleaseACK(t *testing.T) {
	registry := executionregistry.New()
	dispatcher := &ackOrderedRouteDispatcher{}
	flusher := internalhome.NewReleaseFlusher(func() internalconfig.CredentialConcurrencyConfig {
		return internalconfig.CredentialConcurrencyConfig{
			ReleaseFlushInterval: time.Millisecond,
			ReleaseMaxBackoff:    10 * time.Millisecond,
		}
	}, func(_ context.Context, _ internalhome.ConcurrencyReleaseFrame) error {
		dispatcher.acks.Add(1)
		return nil
	})
	registry.SetReleaseSink(flusher.MarkDirty)
	releaseCtx, cancelRelease := context.WithCancel(context.Background())
	releaseDone := make(chan struct{})
	go func() {
		defer close(releaseDone)
		flusher.Run(releaseCtx)
	}()
	defer func() {
		cancelRelease()
		<-releaseDone
	}()

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, registry, 1)
	executor := &retainedRouteModelExecutor{}
	manager.RegisterExecutor(executor)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "retained-route-ack",
		cliproxyexecutor.PinnedAuthMetadataKey:       "retained-route-auth",
	}}
	for _, model := range []string{"alias-a", "alias-a(high)", "alias-a", "alias-a(custom)"} {
		response, errExecute := manager.Execute(ctx, []string{"retained-route"}, cliproxyexecutor.Request{Model: model}, opts)
		if errExecute != nil {
			t.Fatalf("Execute(%q) error = %v", model, errExecute)
		}
		if got := string(response.Payload); got != `{"model":"`+model+`"}` {
			t.Fatalf("Execute(%q) response = %s, want response alias", model, got)
		}
	}
	if got := dispatcher.calls.Load(); got != 2 {
		t.Fatalf("Home RPOP calls = %d, want 2 because custom suffix must redispatch", got)
	}
	if !dispatcher.ackedBeforeSecondRPop.Load() {
		t.Fatal("Home release PUSH was not acknowledged before the second RPOP")
	}
	if got := executor.Models(); !reflect.DeepEqual(got, []string{"target-a", "target-a(high)", "target-a", "target-custom"}) {
		t.Fatalf("executor models = %#v", got)
	}

	manager.CloseExecutionSession("retained-route-ack")
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for dispatcher.acks.Load() != 2 {
		select {
		case <-deadline.C:
			t.Fatalf("final release acknowledgements = %d, want 2", dispatcher.acks.Load())
		case <-time.After(time.Millisecond):
		}
	}
}

type ackOrderedRouteDispatcher struct {
	calls                 atomic.Int32
	acks                  atomic.Int32
	ackedBeforeSecondRPop atomic.Bool
}

func (*ackOrderedRouteDispatcher) HeartbeatOK() bool { return true }

func (d *ackOrderedRouteDispatcher) RPopAuth(_ context.Context, model string, _ string, _ http.Header, _ int) ([]byte, error) {
	call := d.calls.Add(1)
	if call == 2 {
		d.ackedBeforeSecondRPop.Store(d.acks.Load() == 1)
	}
	target := "target-a"
	if canonicalHomeConcurrencyModelKey(model) != "alias-a" {
		target = "target-custom"
	}
	return json.Marshal(map[string]any{
		"model":          target,
		"force_mapping":  true,
		"original_alias": model,
		"auth_index":     "retained-route-auth",
		"auth": Auth{
			ID:       "retained-route-auth",
			Provider: "retained-route",
			Status:   StatusActive,
			Attributes: map[string]string{
				"websockets": "true",
			},
		},
		"concurrency": homeConcurrencyTuple{Accounted: true, CredentialID: "retained-route-auth", Model: target},
	})
}

func (*ackOrderedRouteDispatcher) AbortAmbiguousDispatch() {}

type retainedRouteModelExecutor struct {
	mu     sync.Mutex
	models []string
}

func (*retainedRouteModelExecutor) Identifier() string { return "retained-route" }
func (e *retainedRouteModelExecutor) Execute(_ context.Context, _ *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.models = append(e.models, req.Model)
	e.mu.Unlock()
	if lifecycle, ok := opts.ExecutionLifecycle.(interface{ Retain() }); ok {
		lifecycle.Retain()
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"model":"` + req.Model + `"}`)}, nil
}
func (*retainedRouteModelExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (*retainedRouteModelExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (*retainedRouteModelExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*retainedRouteModelExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
func (e *retainedRouteModelExecutor) Models() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.models...)
}

func TestHomeRetainedPrefixedRouteRewritesSuffixAndResponse(t *testing.T) {
	registry := executionregistry.New()
	dispatcher := &prefixedRetainedRouteDispatcher{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, registry, 1)
	executor := &prefixedRetainedRouteExecutor{}
	manager.RegisterExecutor(executor)
	t.Cleanup(func() { manager.CloseExecutionSession("prefixed-retained-route") })

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "prefixed-retained-route",
		cliproxyexecutor.PinnedAuthMetadataKey:       "prefixed-retained-route-auth",
	}}
	for _, model := range []string{"team/alias-a", "team/alias-a(high)"} {
		response, errExecute := manager.Execute(ctx, []string{"prefixed-retained-route"}, cliproxyexecutor.Request{Model: model}, opts)
		if errExecute != nil {
			t.Fatalf("Execute(%q) error = %v", model, errExecute)
		}
		if got := string(response.Payload); got != `{"model":"`+model+`"}` {
			t.Fatalf("Execute(%q) response = %s, want external response alias", model, got)
		}
	}
	if got := dispatcher.Models(); !reflect.DeepEqual(got, []string{"team/alias-a"}) {
		t.Fatalf("Home RPOP models = %#v, want external canonical route only", got)
	}
	if got := executor.Models(); !reflect.DeepEqual(got, []string{"target-a", "target-a(high)"}) {
		t.Fatalf("executor models = %#v, want upstream suffix rewrite", got)
	}
	manager.mu.RLock()
	selection := manager.homeSessionSelections["prefixed-retained-route"][homeSessionSelectionKey{
		credentialID: "prefixed-retained-route-auth",
		routeModel:   "team/alias-a",
	}]
	manager.mu.RUnlock()
	if selection == nil {
		t.Fatal("retained selection missing external route key")
	}
	retainedAuth := selection.CloneAuthForRoute("team/alias-a(high)")
	if got := retainedAuth.Attributes[homeOriginalAliasAttributeKey]; got != "alias-a(high)" {
		t.Fatalf("retained original alias = %q, want prefix-stripped alias-a(high)", got)
	}
}

type prefixedRetainedRouteDispatcher struct {
	mu     sync.Mutex
	models []string
}

func (*prefixedRetainedRouteDispatcher) HeartbeatOK() bool { return true }

func (d *prefixedRetainedRouteDispatcher) RPopAuth(_ context.Context, model string, _ string, _ http.Header, _ int) ([]byte, error) {
	d.mu.Lock()
	d.models = append(d.models, model)
	d.mu.Unlock()
	return json.Marshal(map[string]any{
		"model":          "target-a",
		"force_mapping":  true,
		"original_alias": "alias-a",
		"auth_index":     "prefixed-retained-route-auth",
		"auth": Auth{
			ID:       "prefixed-retained-route-auth",
			Provider: "prefixed-retained-route",
			Prefix:   "team",
			Status:   StatusActive,
			Attributes: map[string]string{
				"websockets": "true",
			},
		},
		"concurrency": homeConcurrencyTuple{Accounted: true, CredentialID: "prefixed-retained-route-auth", Model: "target-a"},
	})
}

func (*prefixedRetainedRouteDispatcher) AbortAmbiguousDispatch() {}

func (d *prefixedRetainedRouteDispatcher) Models() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.models...)
}

type prefixedRetainedRouteExecutor struct {
	mu     sync.Mutex
	models []string
}

func (*prefixedRetainedRouteExecutor) Identifier() string { return "prefixed-retained-route" }
func (e *prefixedRetainedRouteExecutor) Execute(_ context.Context, _ *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.models = append(e.models, req.Model)
	e.mu.Unlock()
	if lifecycle, ok := opts.ExecutionLifecycle.(interface{ Retain() }); ok {
		lifecycle.Retain()
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"model":"` + req.Model + `"}`)}, nil
}
func (*prefixedRetainedRouteExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (*prefixedRetainedRouteExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (*prefixedRetainedRouteExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*prefixedRetainedRouteExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
func (e *prefixedRetainedRouteExecutor) Models() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.models...)
}
func TestHomeRedispatchStopsWhenReleaseAcknowledgementFails(t *testing.T) {
	registry := executionregistry.New()
	dispatcher := &ackOrderedRouteDispatcher{}
	flusher := internalhome.NewReleaseFlusher(func() internalconfig.CredentialConcurrencyConfig {
		return internalconfig.CredentialConcurrencyConfig{
			CPACancelBound:       20 * time.Millisecond,
			ReleaseFlushInterval: time.Millisecond,
			ReleaseMaxBackoff:    time.Millisecond,
		}
	}, func(context.Context, internalhome.ConcurrencyReleaseFrame) error {
		return context.DeadlineExceeded
	})
	registry.SetReleaseSink(flusher.MarkDirty)
	releaseCtx, cancelRelease := context.WithCancel(context.Background())
	releaseDone := make(chan struct{})
	go func() {
		defer close(releaseDone)
		flusher.Run(releaseCtx)
	}()
	defer func() {
		cancelRelease()
		<-releaseDone
	}()

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		Home: internalconfig.HomeConfig{Enabled: true},
		CredentialConcurrency: internalconfig.CredentialConcurrencyConfig{
			CPACancelBound:       20 * time.Millisecond,
			ReleaseFlushInterval: time.Millisecond,
			ReleaseMaxBackoff:    time.Millisecond,
		},
	})
	manager.PublishHomeDispatch(dispatcher, registry, 1)
	manager.RegisterExecutor(&retainedRouteModelExecutor{})
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "release-failure",
		cliproxyexecutor.PinnedAuthMetadataKey:       "retained-route-auth",
	}}
	if _, errExecute := manager.Execute(ctx, []string{"retained-route"}, cliproxyexecutor.Request{Model: "alias-a"}, opts); errExecute != nil {
		t.Fatalf("first Execute() error = %v", errExecute)
	}
	if _, errExecute := manager.Execute(ctx, []string{"retained-route"}, cliproxyexecutor.Request{Model: "alias-a(custom)"}, opts); errExecute == nil {
		t.Fatal("redispatch after unacknowledged release unexpectedly succeeded")
	}
	if got := dispatcher.calls.Load(); got != 1 {
		t.Fatalf("Home RPOP calls = %d, want no second RPOP after release failure", got)
	}
}

func TestHomeForceMappingAliasResultRequiresExplicitFlag(t *testing.T) {
	auth := &Auth{
		Provider: "xai",
		Attributes: map[string]string{
			homeUpstreamModelAttributeKey: "grok-4.5",
			homeOriginalAliasAttributeKey: "grok-latest",
		},
	}

	result := homeForceMappingAliasResult(auth, "grok-latest")
	if result.ForceMapping || result.OriginalAlias != "" {
		t.Fatalf("homeForceMappingAliasResult() = %+v, want no force mapping", result)
	}
}
