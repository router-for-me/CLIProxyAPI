package codexreset

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

var testNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func testAuth(subject string) *coreauth.Auth {
	claims := fmt.Sprintf(`{"sub":%q,"https://api.openai.com/auth":{"chatgpt_account_id":"workspace"}}`, subject)
	return &coreauth.Auth{ID: subject, Provider: "codex", Metadata: map[string]any{
		"id_token": "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig", "account_id": "workspace",
	}}
}

type fakeManager struct {
	mu              sync.Mutex
	auth            *coreauth.Auth
	consume, clear  int
	answer          string
	fail, clearFail bool
	status          int
	readFail        bool
	beforeConsume   func()
	closeErr        error
}

type responseBody struct {
	io.Reader
	closeErr error
}

func (b *responseBody) Close() error { return b.closeErr }

func (m *fakeManager) List() []*coreauth.Auth                { return []*coreauth.Auth{m.auth} }
func (m *fakeManager) GetByID(string) (*coreauth.Auth, bool) { return m.auth, true }
func (m *fakeManager) ResetQuota(context.Context, string) (*coreauth.Auth, []string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clearFail {
		return nil, nil, errors.New("storage offline")
	}
	m.clear++
	return m.auth, nil, nil
}
func (m *fakeManager) HttpRequest(ctx context.Context, a *coreauth.Auth, req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a != m.auth || !coreauth.IsCodexClientRequest(ctx) || req.Header.Get("ChatGPT-Account-Id") != "workspace" {
		return nil, errors.New("wrong identity or token partition")
	}
	var body string
	switch req.URL.Path {
	case "/backend-api/wham/usage":
		if m.readFail {
			return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"7200"}}, Body: io.NopCloser(strings.NewReader("rate limited"))}, nil
		}
		body = fmt.Sprintf(`{"rate_limit":{"primary_window":{"limit_window_seconds":18000,"used_percent":90,"reset_at":%d}}}`, testNow.Add(2*time.Hour).Unix())
	case "/backend-api/wham/rate-limit-reset-credits":
		if m.beforeConsume != nil {
			m.beforeConsume()
		}
		body = creditResponse()
	case "/backend-api/wham/rate-limit-reset-credits/consume":
		m.consume++
		raw, _ := io.ReadAll(req.Body)
		if ojson.GetBytes(raw, "credit_id").String() != "soon" || ojson.GetBytes(raw, "redeem_request_id").String() == "" {
			return nil, errors.New("missing selected credit or idempotency key")
		}
		if m.fail {
			return nil, errors.New("response lost after upstream debit")
		}
		body = m.answer
		if m.status != 0 {
			return &http.Response{StatusCode: m.status, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
	default:
		return nil, errors.New("unexpected endpoint")
	}
	return &http.Response{StatusCode: 200, Body: &responseBody{Reader: strings.NewReader(body), closeErr: m.closeErr}}, nil
}

func creditResponse() string {
	return fmt.Sprintf(`{"available_count":2,"applicable_available_count":2,"credits":[{"id":"later","reset_type":"codex_rate_limits","status":"available","expires_at":%q},{"id":"soon","reset_type":"codex_rate_limits","status":"available","expires_at":%q}]}`, testNow.Add(50*time.Minute).Format(time.RFC3339), testNow.Add(10*time.Minute).Format(time.RFC3339))
}

func setup(t *testing.T, enabled bool) (*Worker, *fakeManager, string) {
	t.Helper()
	store, errOpen := kvstore.OpenBoltStore(filepath.Join(t.TempDir(), "kv.db"))
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager := &fakeManager{auth: testAuth("user"), answer: `{"code":"reset","windows_reset":0}`}
	key := AccountKey(manager.auth)
	state, raw, exists, errRead := Read(context.Background(), store, key)
	if errRead != nil {
		t.Fatal(errRead)
	}
	state.Enabled = enabled
	if saved, errSave := Save(context.Background(), store, state, raw, exists); !saved || errSave != nil {
		t.Fatalf("save: %v", errSave)
	}
	return &Worker{Manager: manager, Store: store, Now: func() time.Time { return testNow }}, manager, key
}

func TestSelectCredit(t *testing.T) {
	usage := fmt.Sprintf(`{"rate_limit":{"primary_window":{"limit_window_seconds":18000,"used_percent":21,"reset_at":%d}}}`, testNow.Add(time.Hour).Unix())
	for _, tc := range []struct{ name, usage, credits, want string }{
		{"earliest", usage, creditResponse(), "soon"},
		{"official payload without optional applicability", usage, strings.Replace(creditResponse(), `"applicable_available_count":2,`, "", 1), "soon"},
		{"usage explicitly denies applicability", strings.Replace(usage, `{"rate_limit":`, `{"rate_limit_reset_credits":{"applicable_available_count":0},"rate_limit":`, 1), creditResponse(), ""},
		{"unknown covered window", strings.Replace(usage, "18000", "12345", 1), creditResponse(), ""},
		{"explicit applicability denial", usage, strings.Replace(creditResponse(), `"applicable_available_count":2`, `"applicable_available_count":0`, 1), ""},
		{"null applicability", usage, strings.Replace(creditResponse(), `"applicable_available_count":2`, `"applicable_available_count":null`, 1), ""},
		{"no usage", "{}", creditResponse(), ""},
		{"no allowance restored", strings.Replace(usage, ":21", ":20", 1), creditResponse(), ""},
		{"malformed percentage", strings.Replace(usage, ":21", `:"90"`, 1), creditResponse(), ""},
		{"expired window", strings.Replace(usage, fmt.Sprint(testNow.Add(time.Hour).Unix()), "1", 1), creditResponse(), ""},
		{"not applicable", usage, strings.Replace(creditResponse(), "count\":2", "count\":0", 1), ""},
		{"unknown eligibility", usage, `{"credits":[]}`, ""},
		{"string eligibility", usage, strings.Replace(creditResponse(), "count\":2", `count":"2"`, 1), ""},
		{"wrong type", usage, strings.ReplaceAll(creditResponse(), "codex_rate_limits", "paid_credit"), ""},
		{"expired credits", usage, strings.ReplaceAll(creditResponse(), "2026-11-10", "2025-11-10"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := selectCredit(ojson.Parse(tc.usage), ojson.Parse(tc.credits), testNow, time.Hour); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestWorkerDisabledByDefault(t *testing.T) {
	w, m, _ := setup(t, false)
	w.Check(context.Background())
	if m.consume != 0 || m.clear != 0 {
		t.Fatal("disabled policy spent a reset")
	}
}

func TestWorkerConfirmedOutcomes(t *testing.T) {
	for _, code := range []string{"reset", "already_redeemed", "nothing_to_reset", "no_credit", "unknown"} {
		t.Run(code, func(t *testing.T) {
			w, m, key := setup(t, true)
			m.answer = fmt.Sprintf(`{"code":%q,"windows_reset":0}`, code)
			w.Check(context.Background())
			state, _, _, errRead := Read(context.Background(), w.Store, key)
			if errRead != nil {
				t.Fatal(errRead)
			}
			confirmed := code == "reset" || code == "already_redeemed"
			if (m.clear == 1) != confirmed || state.Pending != (code == "unknown") || m.consume != 1 {
				t.Fatalf("consume=%d clear=%d state=%+v", m.consume, m.clear, state)
			}
		})
	}
}

func TestWorkerResponseCloseFailuresAreLoggedWithoutChangingRedemption(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(fmt.Sprint(fails), func(t *testing.T) {
			w, m, key := setup(t, true)
			const sensitive = "private credential in transport error"
			if fails {
				m.closeErr = errors.New(sensitive)
			}
			logger := log.StandardLogger()
			previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
			previousLevel := logger.GetLevel()
			hook := logtest.NewLocal(logger)
			logger.SetLevel(log.ErrorLevel)
			t.Cleanup(func() {
				logger.ReplaceHooks(previousHooks)
				logger.SetLevel(previousLevel)
			})
			w.Check(context.Background())
			wantLogs := 0
			if fails {
				wantLogs = 3 // Usage, available credits, and confirmed redemption each close their body.
			}
			entries := hook.AllEntries()
			if len(entries) != wantLogs {
				t.Fatalf("close error logs=%d, want %d", len(entries), wantLogs)
			}
			for _, entry := range entries {
				if entry.Level != log.ErrorLevel || entry.Message == "" || strings.Contains(entry.Message, sensitive) || len(entry.Data) != 0 {
					t.Fatalf("unsafe close diagnostic: %+v", entry)
				}
			}
			state, _, _, errRead := Read(context.Background(), w.Store, key)
			if errRead != nil || state.Result != "reset" || state.Pending || m.consume != 1 || m.clear != 1 {
				t.Fatalf("close failure changed redemption: consume=%d clear=%d state=%+v err=%v", m.consume, m.clear, state, errRead)
			}
			w.Now = func() time.Time { return testNow.Add(interval) }
			w.Check(context.Background())
			if m.consume != 1 {
				t.Fatal("close failure retried a confirmed redemption")
			}
		})
	}
}

func TestWorkerAmbiguousResponseSurvivesRestart(t *testing.T) {
	w, m, key := setup(t, true)
	m.fail = true
	w.Check(context.Background())
	store := w.Store.(*kvstore.BoltStore)
	path := store.Path()
	if errClose := store.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	reopened, errOpen := kvstore.OpenBoltStore(path)
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	w = &Worker{Manager: m, Store: reopened, Now: func() time.Time { return testNow.Add(time.Hour) }}
	w.Check(context.Background())
	state, _, _, _ := Read(context.Background(), w.Store, key)
	if m.consume != 1 || m.clear != 0 || !state.Pending || state.RequestID == "" {
		t.Fatalf("ambiguous redemption retried: %+v", state)
	}
}

func TestWorkerConcurrentProbe(t *testing.T) {
	w, m, _ := setup(t, true)
	var group sync.WaitGroup
	for i := 0; i < 10; i++ {
		group.Add(1)
		go func() { defer group.Done(); w.Check(context.Background()) }()
	}
	group.Wait()
	if m.consume != 1 || m.clear != 1 {
		t.Fatalf("consume=%d clear=%d", m.consume, m.clear)
	}
}

func TestWorkerConsentRevokedDuringProbe(t *testing.T) {
	w, m, key := setup(t, true)
	m.beforeConsume = func() {
		state, raw, exists, _ := Read(context.Background(), w.Store, key)
		state.Enabled = false
		if saved, errSave := Save(context.Background(), w.Store, state, raw, exists); !saved || errSave != nil {
			t.Error("revoke consent failed")
		}
	}
	w.Check(context.Background())
	if m.consume != 0 {
		t.Fatal("spent after consent revoked")
	}
}

func TestWorkerCooldownRetryNeverRedeemsAgain(t *testing.T) {
	w, m, _ := setup(t, true)
	m.clearFail = true
	w.Check(context.Background())
	m.clearFail = false
	w.Now = func() time.Time { return testNow.Add(time.Hour) }
	w.Check(context.Background())
	if m.consume != 1 || m.clear != 1 {
		t.Fatalf("consume=%d clear=%d", m.consume, m.clear)
	}
}

type aliasManager struct {
	*fakeManager
	auths       []*coreauth.Auth
	failID      string
	cleared     map[string]int
	rejectPath  string
	rejectCodes map[string]int
	retryAfter  map[string]string
	requests    map[string]int
}

func (m *aliasManager) List() []*coreauth.Auth { return m.auths }
func (m *aliasManager) GetByID(id string) (*coreauth.Auth, bool) {
	for _, auth := range m.auths {
		if auth.ID == id {
			return auth, true
		}
	}
	return nil, false
}
func (m *aliasManager) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	if m.requests == nil {
		m.requests = map[string]int{}
	}
	m.requests[auth.ID+req.URL.Path]++
	code := m.rejectCodes[auth.ID]
	m.mu.Unlock()
	if code != 0 && req.URL.Path == "/backend-api/wham/"+m.rejectPath {
		return &http.Response{StatusCode: code, Header: http.Header{"Retry-After": []string{m.retryAfter[auth.ID]}}, Body: io.NopCloser(strings.NewReader("rejected"))}, nil
	}
	return m.fakeManager.HttpRequest(ctx, m.auth, req)
}
func (m *aliasManager) ResetQuota(ctx context.Context, id string) (*coreauth.Auth, []string, error) {
	if id == m.failID {
		return nil, nil, errors.New("storage offline")
	}
	m.cleared[id]++
	return m.fakeManager.ResetQuota(ctx, id)
}

func TestWorkerFallsBackOnlyOnReadAuthenticationFailures(t *testing.T) {
	for _, path := range []string{"usage", "rate-limit-reset-credits", "rate-limit-reset-credits/consume"} {
		for _, code := range []int{401, 403, 429, 500} {
			t.Run(fmt.Sprintf("%s/%d", path, code), func(t *testing.T) {
				w, m, key := setup(t, true)
				stale, disabled, other := testAuth("user"), testAuth("user"), testAuth("another-user")
				stale.ID, disabled.ID, disabled.Disabled = "stale", "disabled", true
				manager := &aliasManager{fakeManager: m, auths: []*coreauth.Auth{stale, disabled, other, m.auth}, cleared: map[string]int{}, rejectPath: path, rejectCodes: map[string]int{stale.ID: code}}
				if path == "rate-limit-reset-credits/consume" {
					manager.rejectCodes = nil
					m.status = code
				}
				w.Manager = manager
				w.Check(context.Background())
				state, _, _, errRead := Read(context.Background(), w.Store, key)
				fallback := path != "rate-limit-reset-credits/consume" && code == 401
				wantResult, wantConsume := "read_failed", 0
				if fallback {
					wantResult, wantConsume = "reset", 1
				} else if path == "rate-limit-reset-credits/consume" {
					wantResult, wantConsume = "unknown", 1
				}
				if errRead != nil || state.Result != wantResult || m.consume != wantConsume || state.Pending != (wantResult == "unknown") {
					t.Fatalf("consume=%d state=%+v err=%v", m.consume, state, errRead)
				}
				if (manager.requests[m.auth.ID+"/backend-api/wham/usage"] == 1) != fallback {
					t.Fatalf("unexpected alias fallback: %v", manager.requests)
				}
				if fallback && (manager.requests[m.auth.ID+"/backend-api/wham/rate-limit-reset-credits"] != 1 || manager.requests[m.auth.ID+"/backend-api/wham/rate-limit-reset-credits/consume"] != 1) {
					t.Fatalf("reads and redemption did not use the working alias: %v", manager.requests)
				}
				for request := range manager.requests {
					if strings.HasPrefix(request, disabled.ID+"/") || strings.HasPrefix(request, other.ID+"/") {
						t.Fatalf("probed disabled or unrelated account: %v", manager.requests)
					}
				}
				before := maps.Clone(manager.requests)
				w.Check(context.Background())
				if !maps.Equal(manager.requests, before) || m.consume != wantConsume {
					t.Fatal("shared claim or pending journal allowed a repeated probe/redemption")
				}
			})
		}
	}
}

func TestWorkerBacksOffAfterAllAliasesAreUnauthorized(t *testing.T) {
	for _, retryAfter := range []string{"", "7200"} {
		t.Run("retryAfter="+retryAfter, func(t *testing.T) {
			w, m, key := setup(t, true)
			alias := testAuth("user")
			alias.ID = "alias"
			manager := &aliasManager{fakeManager: m, auths: []*coreauth.Auth{m.auth, alias}, rejectPath: "usage", rejectCodes: map[string]int{m.auth.ID: 401, alias.ID: 401}, retryAfter: map[string]string{m.auth.ID: retryAfter}}
			w.Manager = manager
			w.Check(context.Background())
			state, _, _, errRead := Read(context.Background(), w.Store, key)
			wantNext := testNow.Add(2 * interval).Unix()
			if retryAfter != "" {
				wantNext = testNow.Add(2 * time.Hour).Unix()
			}
			if errRead != nil || state.Result != "read_failed" || state.Failures != 1 || state.NextCheckAt != wantNext || m.consume != 0 || len(manager.requests) != 2 {
				t.Fatalf("requests=%v state=%+v err=%v", manager.requests, state, errRead)
			}
		})
	}
}

func TestWorkerClearsEveryAccountAlias(t *testing.T) {
	for _, code := range []string{"reset", "already_redeemed"} {
		t.Run(code, func(t *testing.T) {
			w, m, key := setup(t, true)
			m.answer = fmt.Sprintf(`{"code":%q}`, code)
			alias := testAuth("user")
			alias.ID, alias.Prefix = "prefixed-alias", "team"
			disabled := testAuth("user")
			disabled.ID, disabled.Disabled = "disabled-alias", true
			otherWorkspace := testAuth("user")
			otherWorkspace.ID = "other-workspace"
			otherWorkspace.Metadata["account_id"] = "other-workspace"
			// Keep both immutable claims consistent, so isolation is tested against a valid account.
			claims := `{"sub":"user","https://api.openai.com/auth":{"chatgpt_account_id":"other-workspace"}}`
			otherWorkspace.Metadata["id_token"] = "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig"
			manager := &aliasManager{fakeManager: m, auths: []*coreauth.Auth{m.auth, alias, disabled, testAuth("another-user"), otherWorkspace}, cleared: map[string]int{}}
			w.Manager = manager
			w.Check(context.Background())
			state, _, _, errRead := Read(context.Background(), w.Store, key)
			if errRead != nil || state.Result != "reset" || m.consume != 1 || len(manager.cleared) != 3 {
				t.Fatalf("consume=%d cleared=%v state=%+v err=%v", m.consume, manager.cleared, state, errRead)
			}
			for _, id := range []string{"user", "prefixed-alias", "disabled-alias"} {
				if manager.cleared[id] != 1 {
					t.Fatalf("credential %q not cleared exactly once: %v", id, manager.cleared)
				}
			}
		})
	}
}

func TestWorkerAliasCleanupFailureRetriesWithoutRedemption(t *testing.T) {
	w, m, key := setup(t, true)
	alias, sibling := testAuth("user"), testAuth("user")
	alias.ID, sibling.ID = "alias", "sibling"
	manager := &aliasManager{fakeManager: m, auths: []*coreauth.Auth{m.auth, alias, sibling}, failID: alias.ID, cleared: map[string]int{}}
	w.Manager = manager
	w.Check(context.Background())
	state, _, _, errRead := Read(context.Background(), w.Store, key)
	if errRead != nil || state.Result != "cooldown_pending" || state.Pending || m.consume != 1 || manager.cleared["user"] != 1 || manager.cleared["sibling"] != 1 || manager.cleared["alias"] != 0 {
		t.Fatalf("consume=%d cleared=%v state=%+v err=%v", m.consume, manager.cleared, state, errRead)
	}
	// Cleanup survives the original credential's removal and revoked redemption consent.
	state.Enabled = false
	_, raw, exists, _ := Read(context.Background(), w.Store, key)
	if saved, errSave := Save(context.Background(), w.Store, state, raw, exists); !saved || errSave != nil {
		t.Fatalf("disable: %v", errSave)
	}
	manager.auths, manager.failID = []*coreauth.Auth{alias, sibling}, ""
	w.Now = func() time.Time { return testNow.Add(interval) }
	w.Check(context.Background())
	state, _, _, errRead = Read(context.Background(), w.Store, key)
	if errRead != nil || state.Result != "reset" || state.Enabled || state.Pending || m.consume != 1 || manager.cleared["alias"] != 1 || manager.cleared["sibling"] != 2 {
		t.Fatalf("cleanup retry spent or failed: consume=%d cleared=%v state=%+v err=%v", m.consume, manager.cleared, state, errRead)
	}
}

func TestAccountIdentity(t *testing.T) {
	a, b := testAuth("user"), testAuth("another-user")
	if AccountKey(a) == "" || AccountKey(a) == AccountKey(b) {
		t.Fatal("user identity not bound")
	}
	a.Metadata["account_id"] = "replaced-workspace"
	if AccountKey(a) != "" {
		t.Fatal("mismatched workspace accepted")
	}
	for _, header := range []string{"Authorization", "chatgpt-account-id", "Host"} {
		a = testAuth("user")
		a.Attributes = map[string]string{"header:" + header: "override"}
		if AccountKey(a) != "" {
			t.Fatalf("unsafe header override %q accepted", header)
		}
	}
}

func TestWorkerFailedHTTPAndMalformedRepliesRemainPending(t *testing.T) {
	for _, status := range []int{200, 400, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			w, m, key := setup(t, true)
			m.status, m.answer = status, "not JSON"
			w.Check(context.Background())
			state, _, _, _ := Read(context.Background(), w.Store, key)
			if !state.Pending || m.clear != 0 || m.consume != 1 {
				t.Fatalf("unsafe result: %+v", state)
			}
		})
	}
}

func TestWorkerRespectsRetryAfter(t *testing.T) {
	w, m, key := setup(t, true)
	m.readFail = true
	w.Check(context.Background())
	state, _, _, _ := Read(context.Background(), w.Store, key)
	if m.consume != 0 || state.Result != "read_failed" || state.NextCheckAt != testNow.Add(2*time.Hour).Unix() {
		t.Fatalf("backoff: %+v", state)
	}
}

func TestWorkerDoesNotReuseKnownRedeemedCredit(t *testing.T) {
	w, m, _ := setup(t, true)
	w.Check(context.Background())
	w.Now = func() time.Time { return testNow.Add(5 * time.Minute) }
	w.Check(context.Background())
	if m.consume != 1 {
		t.Fatal("retried a known redeemed credit from a stale listing")
	}
}

func TestWorkerRejectsStaleProbeAndChangedIdentity(t *testing.T) {
	for _, changed := range []bool{false, true} {
		w, m, _ := setup(t, true)
		m.beforeConsume = func() {
			if changed {
				m.auth = testAuth("replacement")
			} else {
				w.Now = func() time.Time { return testNow.Add(2 * time.Minute) }
			}
		}
		w.Check(context.Background())
		if m.consume != 0 {
			t.Fatal("spent using stale eligibility or replacement identity")
		}
	}
}
