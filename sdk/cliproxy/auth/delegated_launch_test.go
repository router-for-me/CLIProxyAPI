package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	execution "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestDelegatedLaunchEmptyPoolIsUnverified(t *testing.T) {
	m := NewManager(nil, nil, nil)
	p := NewLaunchPool(m)
	r := p.Prepare(context.Background(), "principal", PrepareLaunchRequest{
		ClientRequestID: "request", SessionID: "session", Provider: "codex", Model: "gpt-6.1-sol",
	})
	if r.Status != "unverified" || r.BindingID != "" {
		t.Fatalf("unexpected admission: %+v", r)
	}
}

type launchTestExecutor struct {
	starts      atomic.Int32
	err         error
	streamError bool
	mu          sync.Mutex
	ids         []string
	models      []string
}

func (e *launchTestExecutor) ExecuteDelegatedLaunch(ctx context.Context, a *Auth, r execution.Request, o execution.Options) (execution.Response, *execution.StreamResult, error) {
	if o.Stream {
		stream, err := e.ExecuteStream(ctx, a, r, o)
		return execution.Response{}, stream, err
	}
	response, err := e.Execute(ctx, a, r, o)
	return response, nil, err
}

func (e *launchTestExecutor) Identifier() string { return "codex" }
func (e *launchTestExecutor) PrepareRequest(r *http.Request, _ *Auth) error {
	r.Header.Set("Authorization", "Bearer synthetic-only")
	return nil
}
func (e *launchTestExecutor) Execute(_ context.Context, a *Auth, r execution.Request, o execution.Options) (execution.Response, error) {
	e.starts.Add(1)
	e.mu.Lock()
	e.ids = append(e.ids, a.ID)
	e.models = append(e.models, r.Model)
	e.mu.Unlock()
	if o.Metadata[execution.PinnedAuthMetadataKey] != a.ID {
		return execution.Response{}, errors.New("missing hard pin")
	}
	return execution.Response{Payload: []byte(`{"id":"ok"}`)}, e.err
}
func (e *launchTestExecutor) ExecuteStream(ctx context.Context, a *Auth, r execution.Request, o execution.Options) (*execution.StreamResult, error) {
	_, err := e.Execute(ctx, a, r, o)
	if err != nil && !e.streamError {
		return nil, err
	}
	chunks := make(chan execution.StreamChunk, 1)
	chunks <- execution.StreamChunk{Payload: []byte("data: {}\n\n"), Err: err}
	close(chunks)
	return &execution.StreamResult{Chunks: chunks}, nil
}
func (*launchTestExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) { return a, nil }
func (*launchTestExecutor) CountTokens(context.Context, *Auth, execution.Request, execution.Options) (execution.Response, error) {
	return execution.Response{}, errors.New("unexpected count")
}
func (*launchTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected unmanaged HTTP")
}

type launchTransport struct {
	target    *url.URL
	transport http.RoundTripper
}

func (t launchTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != usageURL {
		return nil, fmt.Errorf("unexpected probe URL")
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	clone.URL = &u
	clone.URL.Scheme, clone.URL.Host = t.target.Scheme, t.target.Host
	return t.transport.RoundTrip(clone)
}
func (t launchTransport) RoundTripperFor(*Auth) http.RoundTripper { return t }

type launchFixture struct {
	pool     *LaunchPool
	manager  *Manager
	executor *launchTestExecutor
	probes   atomic.Int32
	now      time.Time
	handler  func(http.ResponseWriter, *http.Request)
}

func newLaunchFixture(t *testing.T) *launchFixture {
	t.Helper()
	f := &launchFixture{now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC), manager: NewManager(nil, nil, nil), executor: &launchTestExecutor{}}
	f.manager.RegisterExecutor(f.executor)
	f.pool = NewLaunchPool(f.manager)
	f.pool.now = func() time.Time { return f.now }
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(launchUsage(r.Header.Get("Chatgpt-Account-Id"), true, 12, f.now))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.probes.Add(1)
		if r.Method != "GET" || r.URL.Path != "/backend-api/wham/usage" || r.Header.Get("Authorization") != "Bearer synthetic-only" {
			t.Error("invalid read-only probe")
		}
		w.Header().Set("Date", f.now.Format(http.TimeFormat))
		f.handler(w, r)
	}))
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	f.manager.SetRoundTripperProvider(launchTransport{target, server.Client().Transport})
	return f
}
func launchUsage(account string, allowed bool, used int, now time.Time) []byte {
	return []byte(fmt.Sprintf(`{"account_id":%q,"rate_limit":{"allowed":%t,"limit_reached":false,"primary_window":{"used_percent":%d,"reset_at":%d,"limit_window_seconds":18000},"secondary_window":{"used_percent":12,"reset_at":%d,"limit_window_seconds":604800}}}`, account, allowed, used, now.Add(time.Hour).Unix(), now.Add(24*time.Hour).Unix()))
}
func (f *launchFixture) add(t *testing.T, id, account string) {
	t.Helper()
	id = t.Name() + "-" + id
	_, err := f.manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive, Metadata: map[string]any{"type": "codex", "account_id": account, "access_token": "synthetic-only"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "gpt-6.1-sol"}, {ID: "gpt-6-astra"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
}
func launchRequest(id string) PrepareLaunchRequest {
	return PrepareLaunchRequest{ClientRequestID: id, SessionID: "session", Provider: "codex", Model: "gpt-6.1-sol"}
}
func (f *launchFixture) execute(principal string, r LaunchResult, model, session string, stream bool) error {
	_, s, err := f.pool.ExecuteBound(context.Background(), principal, r.BindingID, session, execution.Request{Model: model, Payload: []byte(fmt.Sprintf(`{"model":%q}`, model))}, execution.Options{Stream: stream})
	if s != nil {
		for c := range s.Chunks {
			if c.Err != nil {
				err = c.Err
			}
		}
	}
	return err
}
func requireLaunchRefusal(t *testing.T, err error, code string) {
	t.Helper()
	var refusal *LaunchRefusal
	if !errors.As(err, &refusal) || refusal.Code != code {
		t.Fatalf("want refusal %s, got %v", code, err)
	}
}

func TestDelegatedLaunchQuotaAccounts(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "shared")
	f.add(t, "b", "shared")
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(launchUsage(r.Header.Get("Chatgpt-Account-Id"), false, 12, f.now))
	}
	r := f.pool.Prepare(context.Background(), "client", launchRequest("one"))
	if r.Status != "waiting" || len(r.QuotaDomains) != 1 || r.QuotaDomains[0] != "codex:credential:shared" || f.probes.Load() != 1 {
		t.Fatalf("aliases not blocked: %+v probes=%d", r, f.probes.Load())
	}
	if f.executor.starts.Load() != 0 {
		t.Fatal("prepare generated")
	}
	f.add(t, "c", "independent")
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(launchUsage(r.Header.Get("Chatgpt-Account-Id"), true, 12, f.now))
	}
	request := launchRequest("two")
	request.BlockedQuotaDomains = []string{"codex:credential:shared"}
	r = f.pool.Prepare(context.Background(), "client", request)
	if r.Status != "ready" || r.QuotaDomains[0] != "codex:credential:independent" {
		t.Fatalf("independent account not ready: %+v", r)
	}
	if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
		t.Fatal(err)
	}
	if f.executor.starts.Load() != 1 || !strings.HasSuffix(f.executor.ids[0], "-c") {
		t.Fatal("wrong account dispatched")
	}
}

func TestDelegatedLaunchProbeFailsClosed(t *testing.T) {
	for _, kind := range []string{"identity-mismatch", "ordinary-missing", "window-missing", "percent-malformed", "expired-window", "stale", "timeout", "redirect", "failed", "partial", "model-limit"} {
		t.Run(kind, func(t *testing.T) {
			f := newLaunchFixture(t)
			f.add(t, "a", "account")
			f.handler = func(w http.ResponseWriter, r *http.Request) {
				body := string(launchUsage("account", true, 12, f.now))
				switch kind {
				case "missing-identity":
					body = strings.Replace(body, `"account_id":"account",`, "", 1)
				case "identity-mismatch":
					body = strings.Replace(body, `"account"`, `"other"`, 1)
				case "ordinary-missing":
					body = strings.Replace(body, `"allowed":true,`, "", 1)
				case "window-missing":
					body = `{"account_id":"account","rate_limit":{"allowed":true}}`
				case "percent-malformed":
					body = strings.Replace(body, `"used_percent":12`, `"used_percent":"12"`, 1)
				case "expired-window":
					body = string(launchUsage("account", true, 12, f.now.Add(-48*time.Hour)))
				case "stale":
					w.Header().Set("Date", f.now.Add(-time.Hour).Format(http.TimeFormat))
				case "timeout":
					<-r.Context().Done()
					return
				case "redirect":
					w.Header().Set("Location", "http://unreachable.invalid")
					w.WriteHeader(302)
					return
				case "failed":
					w.WriteHeader(500)
					return
				case "partial":
					body = body[:len(body)-3]
				case "model-limit":
					body = strings.Replace(body, `"account_id":`, `"additional_rate_limits":[{"model":"gpt-6.1-sol","allowed":false}],"account_id":`, 1)
				}
				_, _ = w.Write([]byte(body))
			}
			ctx := context.Background()
			if kind == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			r := f.pool.Prepare(ctx, "client", launchRequest("request"))
			if r.Status != "unverified" || r.BindingID != "" || f.executor.starts.Load() != 0 {
				t.Fatalf("unverified evidence admitted: %+v", r)
			}
		})
	}
}

func TestDelegatedLaunchDispatchGuards(t *testing.T) {
	for _, kind := range []string{"principal", "session", "model", "expired", "replaced", "disabled", "exhausted", "ordinary-false"} {
		t.Run(kind, func(t *testing.T) {
			f := newLaunchFixture(t)
			f.add(t, "a", "account")
			f.add(t, "b", "other")
			r := f.pool.Prepare(context.Background(), "client", launchRequest("request"))
			if r.Status != "ready" {
				t.Fatalf("%+v", r)
			}
			principal, session, model, code := "client", r.SessionID, r.Model, "binding_invalid"
			switch kind {
			case "principal":
				principal = "attacker"
			case "session":
				session = "other"
			case "model":
				model = "gpt-6-astra"
			case "expired":
				f.now = f.now.Add(time.Minute)
			case "replaced", "disabled":
				a, _ := f.manager.GetByID(t.Name() + "-a")
				if kind == "replaced" {
					a.Metadata["access_token"] = "replacement"
				} else {
					a.Disabled = true
				}
				if _, err := f.manager.Update(context.Background(), a); err != nil {
					t.Fatal(err)
				}
				code = "binding_changed"
			case "exhausted", "ordinary-false":
				f.handler = func(w http.ResponseWriter, q *http.Request) {
					used, allowed := 100, true
					if kind == "ordinary-false" {
						used, allowed = 12, false
					}
					_, _ = w.Write(launchUsage(q.Header.Get("Chatgpt-Account-Id"), allowed, used, f.now))
				}
				code = "usage_limit"
			}
			requireLaunchRefusal(t, f.execute(principal, r, model, session, false), code)
			if f.executor.starts.Load() != 0 {
				t.Fatal("denied request generated or rebound")
			}
		})
	}
}

func TestDelegatedLaunchNoRetryAndTypedStreamRefusal(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			f := newLaunchFixture(t)
			f.add(t, "a", "account")
			f.add(t, "b", "other")
			f.manager.SetRetryConfig(3, time.Second, 0)
			r := f.pool.Prepare(context.Background(), "client", launchRequest("request"))
			f.executor.err = launchTestFailure{}
			f.executor.streamError = true
			requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, stream), "usage_limit")
			if f.executor.starts.Load() != 1 || f.executor.models[0] != r.Model || !strings.HasSuffix(f.executor.ids[0], "-a") {
				t.Fatal("rebound or retried")
			}
		})
	}
}

func TestDelegatedLaunchIdempotencyAndBounds(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	entered, release := make(chan struct{}), make(chan struct{})
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_, _ = w.Write(launchUsage("account", true, 12, f.now))
	}
	results := make(chan LaunchResult, 16)
	go func() { results <- f.pool.Prepare(context.Background(), "client", launchRequest("same")) }()
	<-entered
	for i := 0; i < 15; i++ {
		go func() { results <- f.pool.Prepare(context.Background(), "client", launchRequest("same")) }()
	}
	request := launchRequest("same")
	request.Model = "gpt-6-astra"
	if r := f.pool.Prepare(context.Background(), "client", request); r.Code != "idempotency_conflict" {
		t.Fatalf("%+v", r)
	}
	close(release)
	var binding string
	for i := 0; i < 16; i++ {
		r := <-results
		if r.Status != "ready" {
			t.Fatalf("%+v", r)
		}
		if binding != "" && r.BindingID != binding {
			t.Fatal("duplicate binding")
		}
		binding = r.BindingID
	}
	if f.probes.Load() != 1 || f.executor.starts.Load() != 0 {
		t.Fatal("duplicate probe/generation")
	}
	// Returned slices do not mutate the server's idempotency/binding record.
	r := f.pool.Prepare(context.Background(), "client", launchRequest("same"))
	r.QuotaDomains[0] = "attacker"
	if f.pool.Prepare(context.Background(), "client", launchRequest("same")).QuotaDomains[0] == "attacker" {
		t.Fatal("mutable result")
	}
	// No credential candidates: filling the bounded map needs no HTTP requests.
	p := NewLaunchPool(NewManager(nil, nil, nil))
	p.now = f.pool.now
	for i := 0; i < launchLimit; i++ {
		p.Prepare(context.Background(), "client", launchRequest(fmt.Sprint(i)))
	}
	if r := p.Prepare(context.Background(), "client", launchRequest("overflow")); r.Code != "pool_busy" {
		t.Fatalf("unbounded map: %+v", r)
	}
	f.now = f.now.Add(time.Minute)
	if r := p.Prepare(context.Background(), "client", launchRequest("after-expiry")); r.Code == "pool_busy" {
		t.Fatal("expired entries retained")
	}
}

func TestDelegatedLaunchContractContainsNoCredentials(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	r := f.pool.Prepare(context.Background(), "client", launchRequest("request"))
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "synthetic-only") || strings.Contains(string(b), "access_token") || strings.Contains(string(b), t.Name()+"-a") {
		t.Fatal("credential material exposed")
	}
}

func TestDelegatedLaunchConcurrentProbeBoundAndManagerUnlock(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	entered, release := make(chan struct{}, 4), make(chan struct{})
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		_, _ = w.Write(launchUsage("account", true, 12, f.now))
	}
	results := make(chan LaunchResult, 4)
	for i := 0; i < 4; i++ {
		go func(i int) { results <- f.pool.Prepare(context.Background(), "client", launchRequest(fmt.Sprint(i))) }(i)
	}
	for i := 0; i < 4; i++ {
		<-entered
	}
	// A manager mutation completes while all HTTP probes are blocked. This also
	// proves that an identity changed during a probe is never admitted.
	a, _ := f.manager.GetByID(t.Name() + "-a")
	a.Metadata["access_token"] = "new-synthetic-token"
	if _, err := f.manager.Update(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if r := f.pool.Prepare(context.Background(), "client", launchRequest("overflow")); r.Status != "unverified" {
		t.Fatalf("unbounded probe: %+v", r)
	}
	close(release)
	for i := 0; i < 4; i++ {
		if r := <-results; r.Status != "unverified" {
			t.Fatalf("replaced auth admitted: %+v", r)
		}
	}
	if f.probes.Load() != 4 || f.executor.starts.Load() != 0 {
		t.Fatal("probe bound exceeded or generation occurred")
	}
}

func TestDelegatedLaunchCancelledWaiterDoesNotCancelOwner(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	entered, release := make(chan struct{}), make(chan struct{})
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_, _ = w.Write(launchUsage("account", true, 12, f.now))
	}
	result := make(chan LaunchResult, 1)
	go func() { result <- f.pool.Prepare(context.Background(), "client", launchRequest("same")) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := f.pool.Prepare(ctx, "client", launchRequest("same")); r.Code != "prepare_cancelled" {
		t.Fatalf("cancelled waiter: %+v", r)
	}
	close(release)
	if r := <-result; r.Status != "ready" {
		t.Fatalf("owner cancelled: %+v", r)
	}
	if f.probes.Load() != 1 || f.executor.starts.Load() != 0 {
		t.Fatal("duplicate probe or generation")
	}
}

type deadlineLaunchTransport struct{}

func (deadlineLaunchTransport) RoundTripperFor(*Auth) http.RoundTripper {
	return deadlineLaunchTransport{}
}
func (deadlineLaunchTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, context.DeadlineExceeded
}

func TestDelegatedLaunchProbeDeadline(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	f.manager.SetRoundTripperProvider(deadlineLaunchTransport{})
	if r := f.pool.Prepare(context.Background(), "client", launchRequest("deadline")); r.Status != "unverified" || r.BindingID != "" {
		t.Fatalf("timeout admitted: %+v", r)
	}
	if f.executor.starts.Load() != 0 {
		t.Fatal("timeout generated")
	}
}

type launchTestFailure struct{}

func (launchTestFailure) Error() string                      { return "synthetic quota" }
func (launchTestFailure) StatusCode() int                    { return 429 }
func (launchTestFailure) DelegatedFailure() (string, string) { return "usage_limit", "account" }
