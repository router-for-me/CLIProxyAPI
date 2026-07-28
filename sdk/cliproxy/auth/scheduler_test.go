package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type schedulerTestExecutor struct {
	provider string
}

type schedulerLoadStore struct {
	auths []*Auth
}

func (s *schedulerLoadStore) List(context.Context) ([]*Auth, error) {
	return s.auths, nil
}

func (s *schedulerLoadStore) Save(context.Context, *Auth) (string, error) {
	return "", nil
}

func (s *schedulerLoadStore) Delete(context.Context, string) error {
	return nil
}

func (e schedulerTestExecutor) Identifier() string {
	if e.provider != "" {
		return e.provider
	}
	return "test"
}

func (schedulerTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (schedulerTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (schedulerTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (schedulerTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (schedulerTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

type fakePluginScheduler struct {
	resp     pluginapi.SchedulerPickResponse
	handled  bool
	err      error
	calls    int
	requests []pluginapi.SchedulerPickRequest
	pick     func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error)
}

func (s *fakePluginScheduler) PickAuth(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	s.calls++
	s.requests = append(s.requests, req)
	if s.pick != nil {
		return s.pick(ctx, req)
	}
	return s.resp, s.handled, s.err
}

type inactivePluginScheduler struct {
	fakePluginScheduler
}

type authKindHomeDispatcher struct {
	auths  []Auth
	counts []int
}

func (d *authKindHomeDispatcher) HeartbeatOK() bool {
	return true
}

func (d *authKindHomeDispatcher) RPopAuth(_ context.Context, _ string, _ string, _ http.Header, count int) ([]byte, error) {
	d.counts = append(d.counts, count)
	if count < 1 || count > len(d.auths) {
		return nil, home.ErrAuthNotFound
	}
	return json.Marshal(homeAuthDispatchResponse{Auth: d.auths[count-1]})
}

func (*authKindHomeDispatcher) AbortAmbiguousDispatch() {}

func (s *inactivePluginScheduler) HasScheduler() bool {
	return false
}

type trackingSelector struct {
	calls      int
	lastAuthID []string
}

func (s *trackingSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	s.calls++
	s.lastAuthID = s.lastAuthID[:0]
	for _, auth := range auths {
		s.lastAuthID = append(s.lastAuthID, auth.ID)
	}
	if len(auths) == 0 {
		return nil, nil
	}
	return auths[len(auths)-1], nil
}

func newSchedulerForTest(selector Selector, auths ...*Auth) *authScheduler {
	scheduler := newAuthScheduler(selector)
	scheduler.rebuild(auths)
	return scheduler
}

func registerSchedulerModels(t *testing.T, provider string, model string, authIDs ...string) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	for _, authID := range authIDs {
		reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		for _, authID := range authIDs {
			reg.UnregisterClient(authID)
		}
	})
}

func TestSchedulerPick_RoundRobinHighestPriority(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "low", Provider: "gemini", Attributes: map[string]string{"priority": "0"}},
		&Auth{ID: "high-b", Provider: "gemini", Attributes: map[string]string{"priority": "10"}},
		&Auth{ID: "high-a", Provider: "gemini", Attributes: map[string]string{"priority": "10"}},
	)

	want := []string{"high-a", "high-b", "high-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_WeightedRoundRobin(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&WeightedRoundRobinSelector{},
		&Auth{ID: "a", Provider: "gemini", Attributes: map[string]string{AttributeWeight: "5"}},
		&Auth{ID: "b", Provider: "gemini", Attributes: map[string]string{AttributeWeight: "3"}},
		&Auth{ID: "c", Provider: "gemini", Attributes: map[string]string{AttributeWeight: "2"}},
	)

	counts := make(map[string]int)
	for index := 0; index < 100; index++ {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		counts[got.ID]++
	}
	want := map[string]int{"a": 50, "b": 30, "c": 20}
	for authID, wantCount := range want {
		if counts[authID] != wantCount {
			t.Fatalf("auth %q picks = %d, want %d", authID, counts[authID], wantCount)
		}
	}
}

func TestManagerLoad_WeightedRoundRobinUsesPersistedMetadataWeight(t *testing.T) {
	t.Parallel()

	manager := NewManager(&schedulerLoadStore{auths: []*Auth{
		{ID: "a", Provider: "gemini", Metadata: map[string]any{AttributeWeight: float64(5)}},
		{ID: "b", Provider: "gemini", Metadata: map[string]any{AttributeWeight: float64(1)}},
	}}, &WeightedRoundRobinSelector{}, nil)
	if errLoad := manager.Load(context.Background()); errLoad != nil {
		t.Fatalf("Load() error = %v", errLoad)
	}

	counts := make(map[string]int)
	for index := 0; index < 60; index++ {
		got, errPick := manager.scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		counts[got.ID]++
	}
	if counts["a"] != 50 || counts["b"] != 10 {
		t.Fatalf("metadata-weighted picks = %#v, want a:b=50:10", counts)
	}
}

func TestSchedulerPick_WeightedRoundRobinResetsCreditsWhenWeightsChange(t *testing.T) {
	t.Parallel()

	authA := &Auth{ID: "a", Provider: "gemini", Attributes: map[string]string{AttributeWeight: "1000000"}}
	authB := &Auth{ID: "b", Provider: "gemini", Attributes: map[string]string{AttributeWeight: "1"}}
	scheduler := newSchedulerForTest(&WeightedRoundRobinSelector{}, authA, authB)
	for index := 0; index < 1000; index++ {
		if _, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil); errPick != nil {
			t.Fatalf("warmup pickSingle() #%d error = %v", index, errPick)
		}
	}

	authA.Attributes[AttributeWeight] = "1"
	scheduler.upsertAuth(authA)
	counts := make(map[string]int)
	for index := 0; index < 20; index++ {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() after weight change #%d error = %v", index, errPick)
		}
		counts[got.ID]++
	}
	if counts["a"] != 10 || counts["b"] != 10 {
		t.Fatalf("picks after weight change = %#v, want a:b=10:10", counts)
	}
}

func TestSchedulerPick_WeightedWebsocketResetsCreditsWhenWeightsChange(t *testing.T) {
	t.Parallel()

	authA := &Auth{ID: "a", Provider: "codex", Attributes: map[string]string{AttributeWeight: "1000000", "websockets": "true"}}
	authB := &Auth{ID: "b", Provider: "codex", Attributes: map[string]string{AttributeWeight: "1", "websockets": "true"}}
	scheduler := newSchedulerForTest(&WeightedRoundRobinSelector{}, authA, authB)
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	for index := 0; index < 1000; index++ {
		if _, errPick := scheduler.pickSingle(ctx, "codex", "", cliproxyexecutor.Options{}, nil); errPick != nil {
			t.Fatalf("warmup websocket pickSingle() #%d error = %v", index, errPick)
		}
	}

	authA.Attributes[AttributeWeight] = "1"
	scheduler.upsertAuth(authA)
	counts := make(map[string]int)
	for index := 0; index < 20; index++ {
		got, errPick := scheduler.pickSingle(ctx, "codex", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("websocket pickSingle() after weight change #%d error = %v", index, errPick)
		}
		counts[got.ID]++
	}
	if counts["a"] != 10 || counts["b"] != 10 {
		t.Fatalf("websocket picks after weight change = %#v, want a:b=10:10", counts)
	}
}

func TestManagerLegacyWeightedRoundRobinKeepsIndependentAliasPrefixedModelState(t *testing.T) {
	manager := NewManager(nil, &WeightedRoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.SetPluginScheduler(&fakePluginScheduler{})

	auths := []*Auth{
		{ID: "a-heavy", Provider: "gemini", Attributes: map[string]string{AttributeWeight: "3"}},
		{ID: "a-light", Provider: "gemini", Attributes: map[string]string{AttributeWeight: "1"}},
		{ID: "b-light", Provider: "gemini", Attributes: map[string]string{AttributeWeight: "1"}},
		{ID: "b-heavy", Provider: "gemini", Attributes: map[string]string{AttributeWeight: "3"}},
	}
	for _, auth := range auths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}
	registerSchedulerModels(t, "gemini", "team-a/shared", "a-heavy", "a-light")
	registerSchedulerModels(t, "gemini", "team-b/shared", "b-light", "b-heavy")

	counts := make(map[string]int)
	for index := 0; index < 40; index++ {
		for _, model := range []string{"team-a/shared", "team-b/shared"} {
			got, _, errPick := manager.pickNext(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil)
			if errPick != nil {
				t.Fatalf("pickNext(%q) #%d error = %v", model, index, errPick)
			}
			counts[got.ID]++
		}
	}
	want := map[string]int{"a-heavy": 30, "a-light": 10, "b-light": 10, "b-heavy": 30}
	for authID, wantCount := range want {
		if counts[authID] != wantCount {
			t.Fatalf("auth %q picks = %d, want %d; all=%#v", authID, counts[authID], wantCount, counts)
		}
	}
}

func TestSchedulerPick_WeightedRoundRobinSkipsNonPositiveWeightPriorityTier(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&WeightedRoundRobinSelector{},
		&Auth{ID: "excluded", Provider: "gemini", Attributes: map[string]string{"priority": "10", AttributeWeight: "0"}},
		&Auth{ID: "available", Provider: "gemini", Attributes: map[string]string{"priority": "0", AttributeWeight: "1"}},
	)
	got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil || got.ID != "available" {
		t.Fatalf("pickSingle() auth = %#v, want available", got)
	}
}

func TestSchedulerPick_FillFirstSticksToFirstReady(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&FillFirstSelector{},
		&Auth{ID: "b", Provider: "gemini"},
		&Auth{ID: "a", Provider: "gemini"},
		&Auth{ID: "c", Provider: "gemini"},
	)

	for index := 0; index < 3; index++ {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != "a" {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, "a")
		}
	}
}

func TestSchedulerPick_PromotesExpiredCooldownBeforePick(t *testing.T) {
	t.Parallel()

	model := "gemini-2.5-pro"
	registerSchedulerModels(t, "gemini", model, "cooldown-expired")
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{
			ID:       "cooldown-expired",
			Provider: "gemini",
			ModelStates: map[string]*ModelState{
				model: {
					Status:         StatusError,
					Unavailable:    true,
					NextRetryAfter: time.Now().Add(-1 * time.Second),
				},
			},
		},
	)

	got, errPick := scheduler.pickSingle(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickSingle() auth = nil")
	}
	if got.ID != "cooldown-expired" {
		t.Fatalf("pickSingle() auth.ID = %q, want %q", got.ID, "cooldown-expired")
	}
}

func TestSchedulerPick_CodexWebsocketPrefersWebsocketEnabledSubset(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "codex-http", Provider: "codex"},
		&Auth{ID: "codex-ws-a", Provider: "codex", Attributes: map[string]string{"websockets": "true"}},
		&Auth{ID: "codex-ws-b", Provider: "codex", Attributes: map[string]string{"websockets": "true"}},
	)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	want := []string{"codex-ws-a", "codex-ws-b", "codex-ws-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(ctx, "codex", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_XAIWebsocketPrefersWebsocketEnabledSubset(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "xai-http", Provider: "xai"},
		&Auth{ID: "xai-ws-a", Provider: "xai", Attributes: map[string]string{"websockets": "true"}},
		&Auth{ID: "xai-ws-b", Provider: "xai", Attributes: map[string]string{"websockets": "true"}},
	)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	want := []string{"xai-ws-a", "xai-ws-b", "xai-ws-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(ctx, "xai", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_CodexWebsocketPrefersWebsocketEnabledAcrossPriorities(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "codex-http", Provider: "codex", Attributes: map[string]string{"priority": "10"}},
		&Auth{ID: "codex-ws-a", Provider: "codex", Attributes: map[string]string{"priority": "0", "websockets": "true"}},
		&Auth{ID: "codex-ws-b", Provider: "codex", Attributes: map[string]string{"priority": "0", "websockets": "true"}},
	)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	want := []string{"codex-ws-a", "codex-ws-b", "codex-ws-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(ctx, "codex", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_MixedProvidersUsesWeightedProviderRotationOverReadyCandidates(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "gemini-a", Provider: "gemini"},
		&Auth{ID: "gemini-b", Provider: "gemini"},
		&Auth{ID: "claude-a", Provider: "claude"},
	)

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, provider, errPick := scheduler.pickMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestSchedulerPick_MixedProvidersWeightedRoundRobin(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&WeightedRoundRobinSelector{},
		&Auth{ID: "gemini-a", Provider: "gemini", Attributes: map[string]string{AttributeWeight: "5"}},
		&Auth{ID: "claude-b", Provider: "claude", Attributes: map[string]string{AttributeWeight: "3"}},
		&Auth{ID: "claude-c", Provider: "claude", Attributes: map[string]string{AttributeWeight: "2"}},
	)

	counts := make(map[string]int)
	for index := 0; index < 100; index++ {
		got, provider, errPick := scheduler.pickMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() #%d error = %v", index, errPick)
		}
		if got == nil || provider == "" {
			t.Fatalf("pickMixed() #%d returned auth=%v provider=%q", index, got, provider)
		}
		counts[got.ID]++
	}
	want := map[string]int{"gemini-a": 50, "claude-b": 30, "claude-c": 20}
	for authID, wantCount := range want {
		if counts[authID] != wantCount {
			t.Fatalf("auth %q picks = %d, want %d", authID, counts[authID], wantCount)
		}
	}
}

func TestSchedulerPick_MixedProvidersResetsCreditsWhenWeightsChange(t *testing.T) {
	t.Parallel()

	authA := &Auth{ID: "gemini-a", Provider: "gemini", Attributes: map[string]string{AttributeWeight: "1000000"}}
	authB := &Auth{ID: "claude-b", Provider: "claude", Attributes: map[string]string{AttributeWeight: "1"}}
	scheduler := newSchedulerForTest(&WeightedRoundRobinSelector{}, authA, authB)
	providers := []string{"gemini", "claude"}
	for index := 0; index < 1000; index++ {
		if _, _, errPick := scheduler.pickMixed(context.Background(), providers, "", cliproxyexecutor.Options{}, nil); errPick != nil {
			t.Fatalf("warmup pickMixed() #%d error = %v", index, errPick)
		}
	}

	authA.Attributes[AttributeWeight] = "1"
	scheduler.upsertAuth(authA)
	counts := make(map[string]int)
	for index := 0; index < 20; index++ {
		got, _, errPick := scheduler.pickMixed(context.Background(), providers, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() after weight change #%d error = %v", index, errPick)
		}
		counts[got.ID]++
	}
	if counts[authA.ID] != 10 || counts[authB.ID] != 10 {
		t.Fatalf("mixed picks after weight change = %#v, want 10 each", counts)
	}
}

func TestSchedulerPick_MixedProvidersPrefersHighestPriorityTier(t *testing.T) {
	t.Parallel()

	model := "gpt-default"
	registerSchedulerModels(t, "provider-low", model, "low")
	registerSchedulerModels(t, "provider-high-a", model, "high-a")
	registerSchedulerModels(t, "provider-high-b", model, "high-b")

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "low", Provider: "provider-low", Attributes: map[string]string{"priority": "4"}},
		&Auth{ID: "high-a", Provider: "provider-high-a", Attributes: map[string]string{"priority": "7"}},
		&Auth{ID: "high-b", Provider: "provider-high-b", Attributes: map[string]string{"priority": "7"}},
	)

	providers := []string{"provider-low", "provider-high-a", "provider-high-b"}
	wantProviders := []string{"provider-high-a", "provider-high-b", "provider-high-a", "provider-high-b"}
	wantIDs := []string{"high-a", "high-b", "high-a", "high-b"}
	for index := range wantProviders {
		got, provider, errPick := scheduler.pickMixed(context.Background(), providers, model, cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestManager_PickNextMixed_UsesWeightedProviderRotationBeforeCredentialRotation(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, map[string]struct{}{})
		if errPick != nil {
			t.Fatalf("pickNextMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickNextMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickNextMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickNextMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestManager_PickNextMixed_DisallowFreeAuthSkipsCodexFreePlan(t *testing.T) {
	t.Parallel()

	model := "gpt-5.4-mini"
	registerSchedulerModels(t, "codex", model, "codex-a-free", "codex-b-plus")

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-a-free", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}}); errRegister != nil {
		t.Fatalf("Register(codex-a-free) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-b-plus", Provider: "codex", Attributes: map[string]string{"plan_type": "plus"}}); errRegister != nil {
		t.Fatalf("Register(codex-b-plus) error = %v", errRegister)
	}

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.DisallowFreeAuthMetadataKey: true},
	}
	got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"codex"}, model, opts, map[string]struct{}{})
	if errPick != nil {
		t.Fatalf("pickNextMixed() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNextMixed() auth = nil")
	}
	if provider != "codex" {
		t.Fatalf("pickNextMixed() provider = %q, want %q", provider, "codex")
	}
	if got.ID != "codex-b-plus" {
		t.Fatalf("pickNextMixed() auth.ID = %q, want %q", got.ID, "codex-b-plus")
	}
}

func TestManagerPluginSchedulerSelectsAuthID(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-b) error = %v", errRegister)
	}

	scheduler := &fakePluginScheduler{
		resp:    pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-b"},
		handled: true,
	}
	manager.SetPluginScheduler(scheduler)

	got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{Stream: true}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNext() auth = nil")
	}
	if got.ID != "auth-b" {
		t.Fatalf("pickNext() auth.ID = %q, want %q", got.ID, "auth-b")
	}
	if scheduler.calls != 1 {
		t.Fatalf("scheduler.calls = %d, want %d", scheduler.calls, 1)
	}
	if len(scheduler.requests) != 1 {
		t.Fatalf("len(scheduler.requests) = %d, want %d", len(scheduler.requests), 1)
	}
	if !scheduler.requests[0].Stream {
		t.Fatalf("scheduler request Stream = false, want true")
	}
}

func TestManagerSelectAuthByKindSkipsAPIKey(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	for _, candidate := range []*Auth{
		{ID: "codex-api-key", Provider: "codex", Attributes: map[string]string{AttributeAPIKey: "test-key"}},
		{ID: "codex-oauth", Provider: "codex", Metadata: map[string]any{"access_token": "test-token"}},
	} {
		if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", candidate.ID, errRegister)
		}
	}

	scheduler := &fakePluginScheduler{
		resp:    pluginapi.SchedulerPickResponse{Handled: true, AuthID: "codex-api-key"},
		handled: true,
	}
	manager.SetPluginScheduler(scheduler)

	selected, errSelect := manager.SelectAuthByKind(context.Background(), "codex", "", AuthKindOAuth, cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("SelectAuthByKind() error = %v", errSelect)
	}
	if selected == nil || selected.ID != "codex-oauth" {
		t.Fatalf("SelectAuthByKind() auth = %#v, want codex-oauth", selected)
	}
	if scheduler.calls != 1 {
		t.Fatalf("scheduler.calls = %d, want 1", scheduler.calls)
	}
	if len(scheduler.requests) != 1 || len(scheduler.requests[0].Candidates) != 1 || scheduler.requests[0].Candidates[0].ID != "codex-oauth" {
		t.Fatalf("scheduler candidates = %#v, want only codex-oauth", scheduler.requests)
	}
}

func TestManagerSelectAuthByKindWeightedRoundRobinIgnoresIneligibleAPIKeyWeight(t *testing.T) {
	manager := NewManager(nil, &WeightedRoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	for _, candidate := range []*Auth{
		{ID: "api-high", Provider: "codex", Attributes: map[string]string{AttributeAPIKey: "test-key", AttributeWeight: "100"}},
		{ID: "oauth-heavy", Provider: "codex", Attributes: map[string]string{AttributeWeight: "5"}, Metadata: map[string]any{"access_token": "heavy-token"}},
		{ID: "oauth-light", Provider: "codex", Attributes: map[string]string{AttributeWeight: "1"}, Metadata: map[string]any{"access_token": "light-token"}},
	} {
		if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", candidate.ID, errRegister)
		}
	}

	counts := make(map[string]int)
	for index := 0; index < 600; index++ {
		selected, errSelect := manager.SelectAuthByKind(context.Background(), "codex", "", AuthKindOAuth, cliproxyexecutor.Options{})
		if errSelect != nil {
			t.Fatalf("SelectAuthByKind() #%d error = %v", index, errSelect)
		}
		counts[selected.ID]++
	}
	if counts["oauth-heavy"] != 500 || counts["oauth-light"] != 100 || counts["api-high"] != 0 {
		t.Fatalf("weighted OAuth picks = %#v, want oauth-heavy:oauth-light=500:100 and no API key", counts)
	}
}

func TestManagerWeightedRoundRobinDisallowFreeAuthIgnoresFreeWeight(t *testing.T) {
	tests := []struct {
		name  string
		mixed bool
	}{
		{name: "single provider"},
		{name: "mixed providers", mixed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager(nil, &WeightedRoundRobinSelector{}, nil)
			manager.executors["codex"] = schedulerTestExecutor{}
			lightProvider := "codex"
			if tt.mixed {
				lightProvider = "gemini"
				manager.executors["gemini"] = schedulerTestExecutor{provider: "gemini"}
			}
			for _, candidate := range []*Auth{
				{ID: "free-high", Provider: "codex", Attributes: map[string]string{"plan_type": "free", AttributeWeight: "100"}, Metadata: map[string]any{"access_token": "free-token"}},
				{ID: "paid-heavy", Provider: "codex", Attributes: map[string]string{"plan_type": "plus", AttributeWeight: "5"}, Metadata: map[string]any{"access_token": "heavy-token"}},
				{ID: "paid-light", Provider: lightProvider, Attributes: map[string]string{"plan_type": "plus", AttributeWeight: "1"}, Metadata: map[string]any{"access_token": "light-token"}},
			} {
				if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
					t.Fatalf("Register(%s) error = %v", candidate.ID, errRegister)
				}
			}

			opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.DisallowFreeAuthMetadataKey: true}}
			counts := make(map[string]int)
			for index := 0; index < 600; index++ {
				var selected *Auth
				var errPick error
				if tt.mixed {
					selected, _, _, errPick = manager.pickNextMixed(context.Background(), []string{"codex", "gemini"}, "", opts, nil)
				} else {
					selected, _, errPick = manager.pickNext(context.Background(), "codex", "", opts, nil)
				}
				if errPick != nil {
					t.Fatalf("weighted pick #%d error = %v", index, errPick)
				}
				counts[selected.ID]++
			}
			if counts["paid-heavy"] != 500 || counts["paid-light"] != 100 || counts["free-high"] != 0 {
				t.Fatalf("weighted non-free picks = %#v, want paid-heavy:paid-light=500:100 and no free auth", counts)
			}
		})
	}
}

func TestManagerSelectAuthByKindRoundRobinKeepsEligibleRotation(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	for _, candidate := range []*Auth{
		{ID: "api-key", Provider: "codex", Attributes: map[string]string{AttributeAPIKey: "test-key"}},
		{ID: "oauth-a", Provider: "codex", Metadata: map[string]any{"access_token": "token-a"}},
		{ID: "oauth-b", Provider: "codex", Metadata: map[string]any{"access_token": "token-b"}},
	} {
		if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", candidate.ID, errRegister)
		}
	}

	counts := make(map[string]int)
	for index := 0; index < 6; index++ {
		selected, errSelect := manager.SelectAuthByKind(context.Background(), "codex", "", AuthKindOAuth, cliproxyexecutor.Options{})
		if errSelect != nil {
			t.Fatalf("SelectAuthByKind() #%d error = %v", index, errSelect)
		}
		counts[selected.ID]++
	}
	if counts["oauth-a"] != 3 || counts["oauth-b"] != 3 || counts["api-key"] != 0 {
		t.Fatalf("round-robin OAuth picks = %#v, want three picks per OAuth auth and no API key", counts)
	}
}

func TestManagerSelectAuthByKindReturnsErrorWhenUnavailable(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:         "codex-api-key",
		Provider:   "codex",
		Attributes: map[string]string{AttributeAPIKey: "test-key"},
	}); errRegister != nil {
		t.Fatalf("Register(codex-api-key) error = %v", errRegister)
	}

	selected, errSelect := manager.SelectAuthByKind(context.Background(), "codex", "", AuthKindOAuth, cliproxyexecutor.Options{})
	if selected != nil {
		t.Fatalf("SelectAuthByKind() auth = %#v, want nil", selected)
	}
	var authErr *Error
	if !errors.As(errSelect, &authErr) || authErr.Code != "auth_not_found" {
		t.Fatalf("SelectAuthByKind() error = %#v, want auth_not_found", errSelect)
	}
}

func TestManagerSelectAuthByKindRejectsInvalidKind(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	selected, errSelect := manager.SelectAuthByKind(context.Background(), "codex", "", "certificate", cliproxyexecutor.Options{})
	if selected != nil {
		t.Fatalf("SelectAuthByKind() auth = %#v, want nil", selected)
	}
	var authErr *Error
	if !errors.As(errSelect, &authErr) || authErr.Code != "invalid_auth_kind" || authErr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("SelectAuthByKind() error = %#v, want invalid_auth_kind", errSelect)
	}
}

func TestManagerLegacySelectAuthFailsClosedWhenHomeEnabled(t *testing.T) {
	dispatcher := &authKindHomeDispatcher{auths: []Auth{{
		ID:       "home-oauth",
		Provider: "test",
		Metadata: map[string]any{"access_token": "test-token"},
	}}}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetHomeExecutionRegistry(executionregistry.New())
	manager.RegisterExecutor(schedulerTestExecutor{})

	for name, selectAuth := range map[string]func() (*Auth, error){
		"SelectAuth": func() (*Auth, error) {
			return manager.SelectAuth(context.Background(), "test", "model", cliproxyexecutor.Options{})
		},
		"SelectAuthByKind": func() (*Auth, error) {
			return manager.SelectAuthByKind(context.Background(), "test", "model", AuthKindOAuth, cliproxyexecutor.Options{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			selected, errSelect := selectAuth()
			if selected != nil {
				t.Fatalf("%s() auth = %#v, want nil", name, selected)
			}
			var authErr *Error
			if !errors.As(errSelect, &authErr) || authErr.Code != "home_unavailable" || authErr.HTTPStatus != http.StatusServiceUnavailable {
				t.Fatalf("%s() error = %#v, want home_unavailable", name, errSelect)
			}
		})
	}
	if len(dispatcher.counts) != 0 {
		t.Fatalf("legacy selection issued Home RPOP calls: %v", dispatcher.counts)
	}
}

func TestSelectHomeAuthByKindReturnsHomeSelection(t *testing.T) {
	dispatcher := &authKindHomeDispatcher{auths: []Auth{{
		ID:       "home-oauth",
		Provider: "test",
		Metadata: map[string]any{"access_token": "test-token"},
	}}}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher {
		return dispatcher
	}
	t.Cleanup(func() {
		currentHomeDispatcher = oldCurrentHomeDispatcher
	})

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetHomeExecutionRegistry(executionregistry.New())
	manager.RegisterExecutor(schedulerTestExecutor{})

	selection, errSelect := manager.SelectHomeAuthByKind(context.Background(), "test", "gpt-5.4", AuthKindOAuth, cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("SelectHomeAuthByKind() error = %v", errSelect)
	}
	if selection == nil || selection.Auth == nil || selection.Auth.ID != "home-oauth" {
		t.Fatalf("SelectHomeAuthByKind() = %#v, want home-oauth", selection)
	}
	if selection.Executor == nil || selection.Provider != "test" {
		t.Fatalf("selection executor/provider = %#v/%q, want test", selection.Executor, selection.Provider)
	}
	selection.End("test_complete")
}

func TestSelectHomeAuthByKindSkipsProviderMismatch(t *testing.T) {
	dispatcher := &authKindHomeDispatcher{auths: []Auth{
		{ID: "wrong-provider", Provider: "other", Metadata: map[string]any{"access_token": "test-token"}},
		{ID: "matching-provider", Provider: "test", Metadata: map[string]any{"access_token": "test-token"}},
	}}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher {
		return dispatcher
	}
	t.Cleanup(func() {
		currentHomeDispatcher = oldCurrentHomeDispatcher
	})

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetHomeExecutionRegistry(executionregistry.New())
	manager.RegisterExecutor(schedulerTestExecutor{})
	manager.RegisterExecutor(schedulerTestExecutor{provider: "other"})

	selection, errSelect := manager.SelectHomeAuthByKind(context.Background(), "test", "gpt-5.4", AuthKindOAuth, cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("SelectHomeAuthByKind() error = %v", errSelect)
	}
	if selection == nil || selection.Auth == nil || selection.Auth.ID != "matching-provider" {
		t.Fatalf("SelectHomeAuthByKind() = %#v, want matching provider auth", selection)
	}
	if got := dispatcher.counts; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("home auth counts = %v, want [1 2]", got)
	}
	selection.End("test_complete")
}

func TestSelectHomeAuthByKindKeepsLogicalProviderWhenUsingCompatibilityExecutor(t *testing.T) {
	dispatcher := &authKindHomeDispatcher{auths: []Auth{{
		ID:       "compat-auth",
		Provider: "base-url-provider",
		Attributes: map[string]string{
			"base_url":      "https://compat.example.com",
			AttributeAPIKey: "test-key",
		},
	}}}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher {
		return dispatcher
	}
	t.Cleanup(func() {
		currentHomeDispatcher = oldCurrentHomeDispatcher
	})

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetHomeExecutionRegistry(executionregistry.New())
	manager.RegisterExecutor(schedulerTestExecutor{provider: "openai-compatibility"})

	selection, errSelect := manager.SelectHomeAuthByKind(context.Background(), "base-url-provider", "gpt-5.4", AuthKindAPIKey, cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("SelectHomeAuthByKind() error = %v", errSelect)
	}
	if selection == nil || selection.Auth == nil || selection.Auth.ID != "compat-auth" {
		t.Fatalf("SelectHomeAuthByKind() = %#v, want compat-auth", selection)
	}
	if selection.Provider != "base-url-provider" {
		t.Fatalf("selection.Provider = %q, want logical provider base-url-provider", selection.Provider)
	}
	if selection.Executor == nil || selection.Executor.Identifier() != "openai-compatibility" {
		t.Fatalf("selection.Executor = %#v, want openai-compatibility", selection.Executor)
	}
	selection.End("test_complete")
}

func TestPickNextViaHomeEndsPendingOnInvalidAuth(t *testing.T) {
	dispatcher := &authKindHomeDispatcher{auths: []Auth{{Provider: "test"}}}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher {
		return dispatcher
	}
	t.Cleanup(func() {
		currentHomeDispatcher = oldCurrentHomeDispatcher
	})

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	registry := executionregistry.New()
	manager.SetHomeExecutionRegistry(registry)
	manager.RegisterExecutor(schedulerTestExecutor{})

	_, _, _, errPick := manager.pickNextViaHome(context.Background(), "gpt-5.4", cliproxyexecutor.Options{}, nil)
	var authErr *Error
	if !errors.As(errPick, &authErr) || authErr.Code != "invalid_auth" {
		t.Fatalf("pickNextViaHome() error = %v, want invalid_auth", errPick)
	}

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), time.Second)
	defer cancelDrain()
	if errDrain := registry.Drain(drainCtx); errDrain != nil {
		t.Fatalf("Drain() error = %v, pending dispatch was not ended", errDrain)
	}
}

func TestManagerPluginSchedulerSkippedWhenHomeEnabled(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	scheduler := &fakePluginScheduler{
		resp:    pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-a"},
		handled: true,
	}
	manager.SetPluginScheduler(scheduler)

	_, _, _ = manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)

	if scheduler.calls != 0 {
		t.Fatalf("scheduler.calls = %d, want %d", scheduler.calls, 0)
	}
}

func TestManagerInactivePluginSchedulerKeepsFastPath(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-b) error = %v", errRegister)
	}

	scheduler := &inactivePluginScheduler{}
	manager.SetPluginScheduler(scheduler)

	gotA, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() first error = %v", errPick)
	}
	gotB, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() second error = %v", errPick)
	}
	if gotA == nil || gotB == nil {
		t.Fatalf("pickNext() auths = %v, %v; want non-nil", gotA, gotB)
	}
	if gotA.ID != "auth-a" || gotB.ID != "auth-b" {
		t.Fatalf("fast path picks = %q, %q; want auth-a, auth-b", gotA.ID, gotB.ID)
	}
	if scheduler.calls != 0 {
		t.Fatalf("scheduler.calls = %d, want %d", scheduler.calls, 0)
	}
}

func TestManagerPluginSchedulerCalledOutsideManagerLock(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}

	scheduler := &fakePluginScheduler{
		handled: true,
		pick: func(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
			if !manager.mu.TryLock() {
				t.Fatalf("plugin scheduler called while manager lock is held")
			}
			manager.mu.Unlock()
			return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-a"}, true, nil
		},
	}
	manager.SetPluginScheduler(scheduler)

	got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNext() auth = nil")
	}
	if got.ID != "auth-a" {
		t.Fatalf("pickNext() auth.ID = %q, want auth-a", got.ID)
	}
	if scheduler.calls != 1 {
		t.Fatalf("scheduler.calls = %d, want %d", scheduler.calls, 1)
	}
}

func TestManagerPluginSchedulerErrorStopsPick(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}

	scheduler := &fakePluginScheduler{
		handled: true,
		err:     errors.New("tenant denied"),
	}
	manager.SetPluginScheduler(scheduler)

	got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick == nil {
		t.Fatalf("pickNext() error = nil, want tenant denied")
	}
	if errPick.Error() != "tenant denied" {
		t.Fatalf("pickNext() error = %v, want tenant denied", errPick)
	}
	if got != nil {
		t.Fatalf("pickNext() auth = %v, want nil", got)
	}
}

func TestManagerPluginSchedulerFallsBackWhenUnhandledOrUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		resp    pluginapi.SchedulerPickResponse
		handled bool
	}{
		{
			name:    "unhandled",
			resp:    pluginapi.SchedulerPickResponse{Handled: false},
			handled: false,
		},
		{
			name:    "unknown auth id",
			resp:    pluginapi.SchedulerPickResponse{Handled: true, AuthID: "missing"},
			handled: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, &FillFirstSelector{}, nil)
			manager.executors["gemini"] = schedulerTestExecutor{}
			if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
				t.Fatalf("Register(auth-b) error = %v", errRegister)
			}
			if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
				t.Fatalf("Register(auth-a) error = %v", errRegister)
			}

			scheduler := &fakePluginScheduler{resp: tc.resp, handled: tc.handled}
			manager.SetPluginScheduler(scheduler)

			got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
			if errPick != nil {
				t.Fatalf("pickNext() error = %v", errPick)
			}
			if got == nil {
				t.Fatalf("pickNext() auth = nil")
			}
			if got.ID != "auth-a" {
				t.Fatalf("pickNext() auth.ID = %q, want %q", got.ID, "auth-a")
			}
		})
	}
}

func TestManagerPluginSchedulerDelegatesBuiltin(t *testing.T) {
	t.Run("round-robin", func(t *testing.T) {
		manager := NewManager(nil, &FillFirstSelector{}, nil)
		manager.executors["gemini"] = schedulerTestExecutor{}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
			t.Fatalf("Register(auth-a) error = %v", errRegister)
		}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
			t.Fatalf("Register(auth-b) error = %v", errRegister)
		}
		manager.SetPluginScheduler(&fakePluginScheduler{
			resp:    pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinRoundRobin},
			handled: true,
		})

		gotA, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNext() first error = %v", errPick)
		}
		gotB, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNext() second error = %v", errPick)
		}
		if gotA == nil || gotB == nil {
			t.Fatalf("pickNext() auths = %v, %v; want non-nil", gotA, gotB)
		}
		if gotA.ID != "auth-a" || gotB.ID != "auth-b" {
			t.Fatalf("round-robin picks = %q, %q; want auth-a, auth-b", gotA.ID, gotB.ID)
		}
	})

	t.Run("round-robin model cursors", func(t *testing.T) {
		reg := registry.GetGlobalRegistry()
		models := []*registry.ModelInfo{{ID: "model-a"}, {ID: "model-b"}}
		for _, authID := range []string{"auth-a", "auth-b"} {
			reg.RegisterClient(authID, "gemini", models)
			t.Cleanup(func() {
				reg.UnregisterClient(authID)
			})
		}

		manager := NewManager(nil, &FillFirstSelector{}, nil)
		manager.executors["gemini"] = schedulerTestExecutor{}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
			t.Fatalf("Register(auth-a) error = %v", errRegister)
		}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
			t.Fatalf("Register(auth-b) error = %v", errRegister)
		}
		manager.SetPluginScheduler(&fakePluginScheduler{
			resp:    pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinRoundRobin},
			handled: true,
		})

		gotModelA, _, errPick := manager.pickNext(context.Background(), "gemini", "model-a", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNext(model-a) error = %v", errPick)
		}
		gotModelB, _, errPick := manager.pickNext(context.Background(), "gemini", "model-b", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNext(model-b) error = %v", errPick)
		}
		if gotModelA == nil || gotModelB == nil {
			t.Fatalf("pickNext() auths = %v, %v; want non-nil", gotModelA, gotModelB)
		}
		if gotModelA.ID != "auth-a" || gotModelB.ID != "auth-a" {
			t.Fatalf("model-scoped round-robin picks = %q, %q; want auth-a, auth-a", gotModelA.ID, gotModelB.ID)
		}
	})

	t.Run("fill-first", func(t *testing.T) {
		manager := NewManager(nil, &RoundRobinSelector{}, nil)
		manager.executors["gemini"] = schedulerTestExecutor{}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
			t.Fatalf("Register(auth-b) error = %v", errRegister)
		}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
			t.Fatalf("Register(auth-a) error = %v", errRegister)
		}
		manager.SetPluginScheduler(&fakePluginScheduler{
			resp:    pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst},
			handled: true,
		})

		got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNext() error = %v", errPick)
		}
		if got == nil {
			t.Fatalf("pickNext() auth = nil")
		}
		if got.ID != "auth-a" {
			t.Fatalf("fill-first pick = %q, want auth-a", got.ID)
		}
	})
}

func TestManagerPluginSchedulerDelegateRoundRobinUsesNativeMixedRotation(t *testing.T) {
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}
	manager.SetPluginScheduler(&fakePluginScheduler{
		resp:    pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinRoundRobin},
		handled: true,
	})

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNextMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickNextMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickNextMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickNextMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestManagerPluginSchedulerPickNextMixedSelectsProvider(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}
	scheduler := &fakePluginScheduler{
		resp:    pluginapi.SchedulerPickResponse{Handled: true, AuthID: "claude-a"},
		handled: true,
	}
	manager.SetPluginScheduler(scheduler)

	got, executor, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNextMixed() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNextMixed() auth = nil")
	}
	if got.ID != "claude-a" {
		t.Fatalf("pickNextMixed() auth.ID = %q, want claude-a", got.ID)
	}
	if provider != "claude" {
		t.Fatalf("pickNextMixed() provider = %q, want claude", provider)
	}
	if executor == nil {
		t.Fatalf("pickNextMixed() executor = nil")
	}
	if len(scheduler.requests) != 1 {
		t.Fatalf("len(scheduler.requests) = %d, want %d", len(scheduler.requests), 1)
	}
	req := scheduler.requests[0]
	if req.Provider != "" {
		t.Fatalf("scheduler request Provider = %q, want empty for mixed provider pick", req.Provider)
	}
	if len(req.Providers) != 2 || req.Providers[0] != "gemini" || req.Providers[1] != "claude" {
		t.Fatalf("scheduler request Providers = %#v, want [gemini claude]", req.Providers)
	}
}

func TestManagerInactivePluginSchedulerKeepsMixedFastPath(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	scheduler := &inactivePluginScheduler{}
	manager.SetPluginScheduler(scheduler)

	got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNextMixed() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNextMixed() auth = nil")
	}
	if provider != "gemini" {
		t.Fatalf("pickNextMixed() provider = %q, want gemini", provider)
	}
	if got.ID != "gemini-a" {
		t.Fatalf("pickNextMixed() auth.ID = %q, want gemini-a", got.ID)
	}
	if scheduler.calls != 0 {
		t.Fatalf("scheduler.calls = %d, want %d", scheduler.calls, 0)
	}
}

func TestManagerPluginSchedulerCandidatesAreSafeCopies(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	auth := &Auth{
		ID:       "auth-a",
		Provider: "gemini",
		Status:   StatusActive,
		Attributes: map[string]string{
			"access_token": "token-value",
			"api_key":      "api-key-value",
			"cookie":       "cookie-value",
			"priority":     "7",
			"team":         "alpha",
		},
		Metadata: map[string]any{"tenant": "one"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}

	scheduler := &fakePluginScheduler{
		handled: true,
		pick: func(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
			if len(req.Candidates) != 1 {
				t.Fatalf("len(req.Candidates) = %d, want %d", len(req.Candidates), 1)
			}
			candidate := req.Candidates[0]
			if candidate.ID != "auth-a" || candidate.Provider != "gemini" || candidate.Priority != 7 || candidate.Status != string(StatusActive) {
				t.Fatalf("scheduler candidate = %#v, want sanitized auth-a metadata", candidate)
			}
			for _, key := range []string{"access_token", "api_key", "cookie"} {
				if _, ok := candidate.Attributes[key]; ok {
					t.Fatalf("scheduler candidate Attributes contains sensitive key %q", key)
				}
			}
			if candidate.Attributes["priority"] != "7" {
				t.Fatalf("scheduler candidate priority attribute = %q, want 7", candidate.Attributes["priority"])
			}
			if len(candidate.Metadata) != 0 {
				t.Fatalf("scheduler candidate Metadata = %#v, want empty", candidate.Metadata)
			}
			candidate.Attributes["team"] = "mutated"
			req.Candidates[0] = candidate
			return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-a"}, true, nil
		},
	}
	manager.SetPluginScheduler(scheduler)

	if _, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil); errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}

	manager.mu.RLock()
	gotAttr := manager.auths["auth-a"].Attributes["team"]
	gotAPIKey := manager.auths["auth-a"].Attributes["api_key"]
	manager.mu.RUnlock()
	if gotAttr != "alpha" {
		t.Fatalf("manager auth attribute team = %q, want alpha", gotAttr)
	}
	if gotAPIKey != "api-key-value" {
		t.Fatalf("manager auth attribute api_key = %q, want api-key-value", gotAPIKey)
	}
}

func TestManagerCustomSelector_FallsBackToLegacyPath(t *testing.T) {
	t.Parallel()

	selector := &trackingSelector{}
	manager := NewManager(nil, selector, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.auths["auth-a"] = &Auth{ID: "auth-a", Provider: "gemini"}
	manager.auths["auth-b"] = &Auth{ID: "auth-b", Provider: "gemini"}

	got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, map[string]struct{}{})
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNext() auth = nil")
	}
	if selector.calls != 1 {
		t.Fatalf("selector.calls = %d, want %d", selector.calls, 1)
	}
	if len(selector.lastAuthID) != 2 {
		t.Fatalf("len(selector.lastAuthID) = %d, want %d", len(selector.lastAuthID), 2)
	}
	if got.ID != selector.lastAuthID[len(selector.lastAuthID)-1] {
		t.Fatalf("pickNext() auth.ID = %q, want selector-picked %q", got.ID, selector.lastAuthID[len(selector.lastAuthID)-1])
	}
}

func TestManager_InitializesSchedulerForBuiltInSelector(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if manager.scheduler == nil {
		t.Fatalf("manager.scheduler = nil")
	}
	if manager.scheduler.strategy != schedulerStrategyRoundRobin {
		t.Fatalf("manager.scheduler.strategy = %v, want %v", manager.scheduler.strategy, schedulerStrategyRoundRobin)
	}

	manager.SetSelector(&FillFirstSelector{})
	if manager.scheduler.strategy != schedulerStrategyFillFirst {
		t.Fatalf("manager.scheduler.strategy = %v, want %v", manager.scheduler.strategy, schedulerStrategyFillFirst)
	}
}

func TestManager_SchedulerTracksRegisterAndUpdate(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}

	got, errPick := manager.scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() error = %v", errPick)
	}
	if got == nil || got.ID != "auth-a" {
		t.Fatalf("scheduler.pickSingle() auth = %v, want auth-a", got)
	}

	if _, errUpdate := manager.Update(context.Background(), &Auth{ID: "auth-a", Provider: "gemini", Disabled: true}); errUpdate != nil {
		t.Fatalf("Update(auth-a) error = %v", errUpdate)
	}

	got, errPick = manager.scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() after update error = %v", errPick)
	}
	if got == nil || got.ID != "auth-b" {
		t.Fatalf("scheduler.pickSingle() after update auth = %v, want auth-b", got)
	}
}

func TestManager_PickNextMixed_UsesSchedulerRotation(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNextMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickNextMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickNextMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickNextMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestManager_PickNextMixed_SkipsProvidersWithoutExecutors(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNextMixed() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNextMixed() auth = nil")
	}
	if provider != "claude" {
		t.Fatalf("pickNextMixed() provider = %q, want %q", provider, "claude")
	}
	if got.ID != "claude-a" {
		t.Fatalf("pickNextMixed() auth.ID = %q, want %q", got.ID, "claude-a")
	}
}

func TestManager_SchedulerTracksMarkResultCooldownAndRecovery(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("auth-a", "gemini", []*registry.ModelInfo{{ID: "test-model"}})
	reg.RegisterClient("auth-b", "gemini", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient("auth-a")
		reg.UnregisterClient("auth-b")
	})
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-b) error = %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "auth-a",
		Provider: "gemini",
		Model:    "test-model",
		Success:  false,
		Error:    &Error{HTTPStatus: 429, Message: "quota"},
	})

	got, errPick := manager.scheduler.pickSingle(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() after cooldown error = %v", errPick)
	}
	if got == nil || got.ID != "auth-b" {
		t.Fatalf("scheduler.pickSingle() after cooldown auth = %v, want auth-b", got)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "auth-a",
		Provider: "gemini",
		Model:    "test-model",
		Success:  true,
	})

	seen := make(map[string]struct{}, 2)
	for index := 0; index < 2; index++ {
		got, errPick = manager.scheduler.pickSingle(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("scheduler.pickSingle() after recovery #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("scheduler.pickSingle() after recovery #%d auth = nil", index)
		}
		seen[got.ID] = struct{}{}
	}
	if len(seen) != 2 {
		t.Fatalf("len(seen) = %d, want %d", len(seen), 2)
	}
}
