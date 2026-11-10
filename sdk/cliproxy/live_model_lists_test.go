package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func resetLiveModelLists() {
	liveModelLists.mu.Lock()
	liveModelLists.lists = make(map[string]liveModelList)
	liveModelLists.nextRetry = make(map[string]time.Time)
	liveModelLists.inFlight = make(map[string]struct{})
	liveModelLists.mu.Unlock()
	liveModelLists.force.Store(false)
	select {
	case <-liveModelLists.wake:
	default:
	}
}

func liveIDs(models []*ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func hasLiveID(models []*ModelInfo, id string) *ModelInfo {
	for _, model := range models {
		if model.ID == id {
			return model
		}
	}
	return nil
}

func TestParseClaudeLiveModelList(t *testing.T) {
	page := []byte(`{"data":[
		{"type":"model","id":"claude-fable-5-2","display_name":"Claude Fable 5.2","created_at":"2026-11-09T18:30:00Z",
		 "max_input_tokens":1000000,"max_tokens":128000,
		 "capabilities":{"image_input":{"supported":true},
		   "effort":{"supported":true,"low":{"supported":true},"medium":{"supported":true},"high":{"supported":true},"xhigh":{"supported":true},"max":{"supported":true}},
		   "thinking":{"supported":true,"types":{"enabled":{"supported":false},"adaptive":{"supported":true},"disabled":{"supported":false}}}}},
		{"type":"model","id":"claude-haiku-5-5","display_name":"Claude Haiku 5.5","max_input_tokens":200000,"max_tokens":64000,"capabilities":{}}
	],"has_more":false,"last_id":"claude-haiku-5-5"}`)
	list, err := parseClaudeLiveModelList([][]byte{page}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.ids) != 2 || len(list.extra) != 2 {
		t.Fatalf("ids=%v extra=%v", list.ids, liveIDs(list.extra))
	}
	fable := list.extra[0]
	if fable.ContextLength != 1000000 || fable.MaxCompletionTokens != 128000 || fable.OwnedBy != "anthropic" || fable.Type != "claude" {
		t.Fatalf("fable = %+v", fable)
	}
	if fable.Created != time.Date(2026, 11, 9, 18, 30, 0, 0, time.UTC).Unix() {
		t.Fatalf("created = %d", fable.Created)
	}
	if fable.Thinking == nil || strings.Join(fable.Thinking.Levels, ",") != "low,medium,high,xhigh,max" ||
		!fable.Thinking.DynamicAllowed || fable.Thinking.ZeroAllowed {
		t.Fatalf("thinking = %+v", fable.Thinking)
	}
	if strings.Join(fable.SupportedInputModalities, ",") != "text,image" {
		t.Fatalf("modalities = %v", fable.SupportedInputModalities)
	}
	if haiku := list.extra[1]; haiku.Thinking != nil || strings.Join(haiku.SupportedInputModalities, ",") != "text" {
		t.Fatalf("haiku = %+v", haiku)
	}
	if _, errEmpty := parseClaudeLiveModelList([][]byte{[]byte(`{"data":[]}`)}, time.Now()); errEmpty == nil {
		t.Fatal("an empty list must not count as a successful fetch")
	}
}

func TestParseCodexLiveModelList(t *testing.T) {
	body := []byte(`{"models":[
		{"slug":"gpt-6.1-sol","display_name":"GPT-6.1-Sol","description":"Frontier","visibility":"list","context_window":500000,"max_context_window":872000,
		 "input_modalities":["text","image"],"supported_reasoning_levels":[{"effort":"low"},{"effort":"max"},{"effort":"ultra"}]},
		{"slug":"gpt-reserve","display_name":"GPT-Reserve","visibility":"hide","context_window":272000},
		{"slug":"gpt-next","visibility":"list","context_window":272000}
	]}`)
	list, err := parseCodexLiveModelList(body, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.ids) != 3 {
		t.Fatalf("hidden models still count as available: %v", list.ids)
	}
	if hasLiveID(list.extra, "gpt-reserve") != nil {
		t.Fatal("hidden models must not be added")
	}
	sol := hasLiveID(list.extra, "gpt-6.1-sol")
	if sol == nil || sol.ContextLength != 872000 || sol.OwnedBy != "openai" || strings.Join(sol.Thinking.Levels, ",") != "low,max" ||
		strings.Join(sol.SupportedInputModalities, ",") != "text,image" {
		t.Fatalf("sol = %+v", sol)
	}
	next := hasLiveID(list.extra, "gpt-next")
	if next == nil || next.ContextLength != 272000 || next.Thinking != nil || strings.Join(next.SupportedInputModalities, ",") != "text" {
		t.Fatalf("next = %+v", next)
	}
}

func TestParseMetaLiveModelList(t *testing.T) {
	list, err := parseMetaLiveModelList([]byte(`{"data":[{"id":"muse-ember-1.0"},{"id":"Muse-Spark-1.3"}]}`), time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := list.ids["muse-spark-1.3"]; !ok || len(list.ids) != 2 || len(list.extra) != 0 {
		t.Fatalf("ids=%v extra=%v", list.ids, list.extra)
	}
	if _, errBad := parseMetaLiveModelList([]byte(`not json`), time.Now()); errBad == nil {
		t.Fatal("invalid JSON must fail")
	}
}

func TestFilterByLiveModelList(t *testing.T) {
	builtin := registry.WithCodexBuiltins(nil)[0].ID
	catalog := []*ModelInfo{
		{ID: "gpt-6.1-sol", ContextLength: 922000, DisplayName: "catalog"},
		{ID: "gpt-6-chat-thinking"},
		{ID: builtin},
	}
	list := liveModelList{
		ids: map[string]struct{}{"gpt-6.1-sol": {}, "gpt-next": {}},
		extra: []*ModelInfo{
			{ID: "gpt-6.1-sol", ContextLength: 872000, DisplayName: "live"},
			{ID: "gpt-next", ContextLength: 272000, Thinking: &registry.ThinkingSupport{Levels: []string{"low"}}},
		},
	}
	out := filterByLiveModelList("codex", catalog, list)
	if got := strings.Join(liveIDs(out), ","); got != "gpt-6.1-sol,"+builtin+",gpt-next" {
		t.Fatalf("models = %s", got)
	}
	if sol := hasLiveID(out, "gpt-6.1-sol"); sol.DisplayName != "catalog" || sol.ContextLength != 922000 {
		t.Fatalf("catalog values must win: %+v", sol)
	}
	next := hasLiveID(out, "gpt-next")
	next.Thinking.Levels[0] = "changed"
	if list.extra[1].Thinking.Levels[0] != "low" {
		t.Fatal("added models must be copies")
	}
}

func TestApplyLiveModelListLeavesCatalogAlone(t *testing.T) {
	resetLiveModelLists()
	t.Cleanup(resetLiveModelLists)
	catalog := []*ModelInfo{{ID: "claude-opus-5-5"}, {ID: "claude-3-5-haiku-20241022"}}
	oauth := &coreauth.Auth{ID: "claude-login", Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}}
	apiKey := &coreauth.Auth{ID: "claude-key", Provider: "claude", Attributes: map[string]string{"api_key": "sk-test"}}
	on := &config.Config{}
	on.Models.LiveLists = true
	liveModelLists.put("claude-login", liveModelList{identity: liveModelListIdentity(oauth), ids: map[string]struct{}{"claude-opus-5-5": {}}, fetchedAt: time.Now()})
	liveModelLists.put("claude-key", liveModelList{identity: liveModelListIdentity(apiKey), ids: map[string]struct{}{"claude-opus-5-5": {}}, fetchedAt: time.Now()})

	if got := applyLiveModelList(&config.Config{}, oauth, catalog); len(got) != 2 {
		t.Fatalf("option off: %v", liveIDs(got))
	}
	if got := applyLiveModelList(on, apiKey, catalog); len(got) != 2 {
		t.Fatalf("API keys keep the catalog: %v", liveIDs(got))
	}
	if got := applyLiveModelList(on, oauth, catalog); strings.Join(liveIDs(got), ",") != "claude-opus-5-5" {
		t.Fatalf("login not filtered: %v", liveIDs(got))
	}
	unknown := &coreauth.Auth{ID: "new-login", Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}}
	if got := applyLiveModelList(on, unknown, catalog); len(got) != 2 {
		t.Fatalf("no list yet: %v", liveIDs(got))
	}
	select {
	case <-liveModelLists.wake:
	default:
		t.Fatal("a login without a list must ask for a fetch")
	}
}

func TestLiveModelListDueAndRetry(t *testing.T) {
	resetLiveModelLists()
	t.Cleanup(resetLiveModelLists)
	now := time.Unix(1_000_000, 0)
	if !liveModelLists.due("a", "id-1", now) {
		t.Fatal("never fetched must be due")
	}
	liveModelLists.fail("a", now)
	if liveModelLists.due("a", "id-1", now.Add(liveModelListRetry/2)) || !liveModelLists.due("a", "id-1", now.Add(liveModelListRetry)) {
		t.Fatal("failed fetches wait for the retry delay")
	}
	list := liveModelList{identity: "id-1", ids: map[string]struct{}{"x": {}}, fetchedAt: now}
	if !liveModelLists.put("a", list) || liveModelLists.put("a", list) {
		t.Fatal("put must report changes only")
	}
	if liveModelLists.due("a", "id-1", now.Add(time.Hour)) || !liveModelLists.due("a", "id-1", now.Add(registry.ModelsRefreshInterval)) {
		t.Fatal("lists refresh after ModelsRefreshInterval")
	}
	if !liveModelLists.due("a", "id-2", now.Add(time.Minute)) {
		t.Fatal("a list of another account must be refetched at once")
	}
	if _, ok := liveModelLists.get("a", "id-2"); ok {
		t.Fatal("a list of another account must not apply")
	}
	if !liveModelLists.start("a") || liveModelLists.start("a") || liveModelLists.due("a", "id-2", now) {
		t.Fatal("a running fetch must not be started twice")
	}
	liveModelLists.finish("a")
	liveModelLists.forget(map[string]struct{}{})
	if _, ok := liveModelLists.get("a", "id-1"); ok {
		t.Fatal("lists of removed logins must be dropped")
	}
}

// liveListExecutor answers HttpRequest like the real executors: it signs the request and sends it.
type liveListExecutor struct {
	coreauth.ProviderExecutor
	during      func()
	provider    string
	calls       atomic.Int32
	hadDeadline atomic.Bool
	codexClient atomic.Bool
	accountID   atomic.Value
}

func (e *liveListExecutor) Identifier() string { return e.provider }

func (e *liveListExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *liveListExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	e.calls.Add(1)
	_, deadline := ctx.Deadline()
	e.hadDeadline.Store(deadline)
	e.codexClient.Store(coreauth.IsCodexClientRequest(ctx))
	e.accountID.Store(req.Header.Get("Chatgpt-Account-Id"))
	if e.during != nil {
		e.during()
	}
	req.Header.Set("Authorization", "Bearer test")
	return http.DefaultClient.Do(req.WithContext(ctx))
}

func TestRefreshLiveModelListsRegistersCodexLogin(t *testing.T) {
	resetLiveModelLists()
	t.Cleanup(resetLiveModelLists)
	var version atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer test" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		version.Store(r.URL.Query().Get("client_version"))
		_, _ = w.Write([]byte(`{"models":[
			{"slug":"gpt-6.1-sol","visibility":"list","max_context_window":872000},
			{"slug":"gpt-brand-new","display_name":"GPT Brand New","visibility":"list","max_context_window":400000,
			 "supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]}
		]}`))
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Models.LiveLists = true
	svc := &Service{cfg: cfg, coreManager: coreauth.NewManager(nil, nil, nil)}
	executor := &liveListExecutor{provider: "codex"}
	svc.coreManager.RegisterExecutor(executor)
	auth, err := svc.coreManager.Register(t.Context(), &coreauth.Auth{ID: "codex-live-test", Provider: "codex",
		Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "pro", "base_url": server.URL},
		Metadata:   map[string]any{"account_id": "acct-1", "use_siwc": true}})
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	svc.registerModelsForAuth(t.Context(), auth)
	if !reg.ClientSupportsModel(auth.ID, "gpt-6.1-sol") || reg.ClientSupportsModel(auth.ID, "gpt-brand-new") {
		t.Fatal("before the first fetch the catalog applies")
	}

	// Re-registration installs the real executors; keep the test one for every fetch.
	refresh := func(force bool) {
		svc.coreManager.RegisterExecutor(executor)
		svc.refreshLiveModelLists(t.Context(), force)
		liveModelLists.fetches.Wait()
	}
	refresh(false)
	if executor.calls.Load() != 1 || version.Load() != codexLiveModelListClientVersion {
		t.Fatalf("calls=%d client_version=%v", executor.calls.Load(), version.Load())
	}
	if executor.hadDeadline.Load() {
		t.Fatal("the fetch must not carry a deadline")
	}
	if !executor.codexClient.Load() || executor.accountID.Load() != "acct-1" {
		t.Fatalf("codex fetch must use the outer ChatGPT login: codex client=%v account=%v", executor.codexClient.Load(), executor.accountID.Load())
	}
	if !reg.ClientSupportsModel(auth.ID, "gpt-brand-new") {
		t.Fatal("new model from the live list not registered")
	}
	for _, model := range reg.GetModelsForClient(auth.ID) {
		if model.ID == "gpt-5.6-luna" {
			t.Fatal("catalog model missing from the live list still registered")
		}
	}

	// Fresh lists aren't fetched again until they're due or a refresh is forced.
	refresh(false)
	if executor.calls.Load() != 1 {
		t.Fatalf("fresh list fetched again: %d calls", executor.calls.Load())
	}
	refresh(true)
	if executor.calls.Load() != 2 {
		t.Fatalf("forced refresh skipped: %d calls", executor.calls.Load())
	}

	// A failing fetch keeps the last list.
	server.Close()
	refresh(true)
	if executor.calls.Load() != 3 {
		t.Fatalf("calls = %d", executor.calls.Load())
	}
	if !reg.ClientSupportsModel(auth.ID, "gpt-brand-new") {
		t.Fatal("a failed fetch must keep the previous list")
	}
}

func TestSameLiveModelListComparesEveryField(t *testing.T) {
	base := func() liveModelList {
		return liveModelList{identity: "a", ids: map[string]struct{}{"m": {}}, extra: []*ModelInfo{{ID: "m", ContextLength: 1,
			SupportedInputModalities: []string{"text"}, Thinking: &registry.ThinkingSupport{Levels: []string{"low"}}}}}
	}
	if !sameLiveModelList(base(), base()) {
		t.Fatal("equal lists")
	}
	for name, change := range map[string]func(*liveModelList){
		"modalities":  func(l *liveModelList) { l.extra[0].SupportedInputModalities = []string{"text", "image"} },
		"description": func(l *liveModelList) { l.extra[0].Description = "new" },
		"created":     func(l *liveModelList) { l.extra[0].Created = 5 },
		"zero":        func(l *liveModelList) { l.extra[0].Thinking.ZeroAllowed = true },
		"dynamic":     func(l *liveModelList) { l.extra[0].Thinking.DynamicAllowed = true },
		"identity":    func(l *liveModelList) { l.identity = "b" },
	} {
		changed := base()
		change(&changed)
		if sameLiveModelList(base(), changed) {
			t.Fatalf("%s change not detected", name)
		}
	}
}

func TestLiveModelListIdentity(t *testing.T) {
	a := &coreauth.Auth{Provider: "claude", Metadata: map[string]any{"email": "a@example.com", "access_token": "t1"}}
	refreshed := &coreauth.Auth{Provider: "claude", Metadata: map[string]any{"email": "a@example.com", "access_token": "t2"}}
	other := &coreauth.Auth{Provider: "claude", Metadata: map[string]any{"email": "b@example.com", "access_token": "t1"}}
	if liveModelListIdentity(a) != liveModelListIdentity(refreshed) {
		t.Fatal("a token refresh must keep the identity")
	}
	if liveModelListIdentity(a) == liveModelListIdentity(other) {
		t.Fatal("another account must change the identity")
	}
	noAccount := &coreauth.Auth{Provider: "meta", Metadata: map[string]any{"access_token": "t1"}}
	replaced := &coreauth.Auth{Provider: "meta", Metadata: map[string]any{"access_token": "t9"}}
	if liveModelListIdentity(noAccount) == liveModelListIdentity(replaced) {
		t.Fatal("without an account field the credential decides")
	}
}

func TestReplacedLoginDiscardsTheOldList(t *testing.T) {
	resetLiveModelLists()
	t.Cleanup(resetLiveModelLists)
	cfg := &config.Config{}
	cfg.Models.LiveLists = true
	old := &coreauth.Auth{ID: "same-file", Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"email": "old@example.com"}}
	replacement := &coreauth.Auth{ID: "same-file", Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"email": "new@example.com"}}
	liveModelLists.put("same-file", liveModelList{identity: liveModelListIdentity(old), ids: map[string]struct{}{"claude-haiku-5-5": {}}, fetchedAt: time.Now()})
	catalog := []*ModelInfo{{ID: "claude-haiku-5-5"}, {ID: "claude-opus-5-5"}}
	if got := applyLiveModelList(cfg, replacement, catalog); len(got) != 2 {
		t.Fatalf("the old account's list was applied to the new one: %v", liveIDs(got))
	}
}

func TestFetchFinishingAfterLoginReplacementIsDiscarded(t *testing.T) {
	resetLiveModelLists()
	t.Cleanup(resetLiveModelLists)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"muse-spark-1.3"}]}`))
	}))
	defer server.Close()
	cfg := &config.Config{}
	cfg.Models.LiveLists = true
	svc := &Service{cfg: cfg, coreManager: coreauth.NewManager(nil, nil, nil)}
	executor := &liveListExecutor{provider: "meta"}
	svc.coreManager.RegisterExecutor(executor)
	auth, err := svc.coreManager.Register(t.Context(), &coreauth.Auth{ID: "meta-file", Provider: "meta",
		Attributes: map[string]string{"auth_kind": "oauth", "base_url": server.URL}, Metadata: map[string]any{"email": "old@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	executor.during = func() {
		replacement := auth.Clone()
		replacement.Metadata = map[string]any{"email": "new@example.com"}
		if _, errUpdate := svc.coreManager.Update(context.Background(), replacement); errUpdate != nil {
			t.Error(errUpdate)
		}
	}
	svc.refreshLiveModelLists(t.Context(), true)
	liveModelLists.fetches.Wait()
	if executor.calls.Load() != 1 {
		t.Fatalf("calls = %d", executor.calls.Load())
	}
	liveModelLists.mu.Lock()
	_, stored := liveModelLists.lists["meta-file"]
	liveModelLists.mu.Unlock()
	if stored {
		t.Fatal("a list fetched for the replaced account was stored")
	}
}
