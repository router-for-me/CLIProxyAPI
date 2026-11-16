package management

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func useQuotaHistoryStore(t *testing.T, store *coreauth.QuotaHistory) {
	t.Helper()
	original := quotaHistorySource
	quotaHistorySource = func() *coreauth.QuotaHistory { return store }
	t.Cleanup(func() { quotaHistorySource = original })
}

func recordWeeklyQuota(store *coreauth.QuotaHistory, authID string, observedAt time.Time, used float64, resetAt time.Time) {
	store.Observe(authID, []coreauth.QuotaWindow{{
		Kind:       coreauth.QuotaWindowWeekly,
		Used:       used,
		ResetAt:    resetAt,
		Period:     7 * 24 * time.Hour,
		ObservedAt: observedAt,
	}}, observedAt)
}

func newQuotaHistoryHandler(t *testing.T, cfg *config.Config, auths ...*coreauth.Auth) (*Handler, *coreauth.Manager) {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	for _, auth := range auths {
		registerAuthForLookupTest(t, manager, auth)
	}
	return NewHandlerWithoutConfigFilePath(cfg, manager), manager
}

func requestQuotaHistory(t *testing.T, h *Handler, query string) (int, quotaHistoryResponse, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/credentials/quota-history"+query, nil)
	h.GetQuotaHistory(ctx)
	var payload quotaHistoryResponse
	if rec.Code == http.StatusOK {
		if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), errUnmarshal)
		}
	}
	return rec.Code, payload, rec.Body.String()
}

func assertQuotaHistoryDisabled(t *testing.T, h *Handler) {
	t.Helper()
	code, payload, body := requestQuotaHistory(t, h, "")
	if code != http.StatusOK || payload.Enabled || payload.Now == "" || payload.Credentials == nil || len(payload.Credentials) != 0 {
		t.Fatalf("status=%d body=%s, want 200 enabled:false with no credentials", code, body)
	}
}

func TestGetQuotaHistoryDisabledWithoutStore(t *testing.T) {
	useQuotaHistoryStore(t, nil)
	h, _ := newQuotaHistoryHandler(t, &config.Config{AuthDir: t.TempDir()})
	assertQuotaHistoryDisabled(t, h)

	useQuotaHistoryStore(t, coreauth.NewQuotaHistory())
	assertQuotaHistoryDisabled(t, NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil))
}

func TestGetQuotaHistoryHomeMode(t *testing.T) {
	store := coreauth.NewQuotaHistory()
	recordWeeklyQuota(store, "claude-a.json", time.Now().Add(-time.Hour), 0.4, time.Now().Add(24*time.Hour))
	useQuotaHistoryStore(t, store)
	cfg := &config.Config{AuthDir: t.TempDir()}
	cfg.Home.Enabled = true
	h, _ := newQuotaHistoryHandler(t, cfg, &coreauth.Auth{ID: "claude-a.json", Provider: "claude", Status: coreauth.StatusActive})
	assertQuotaHistoryDisabled(t, h)
}

func TestGetQuotaHistoryColumnarPointsAndIdentity(t *testing.T) {
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	reset := base.Add(3 * 24 * time.Hour)
	store := coreauth.NewQuotaHistory()
	recordWeeklyQuota(store, "claude-a.json", base, 0.304, reset)
	recordWeeklyQuota(store, "claude-a.json", base.Add(time.Hour), 0.3595, time.Time{})
	useQuotaHistoryStore(t, store)
	h, manager := newQuotaHistoryHandler(t, &config.Config{AuthDir: t.TempDir()}, &coreauth.Auth{
		ID:       "claude-a.json",
		FileName: "Claude-A.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
	})
	registered, ok := manager.GetByID("claude-a.json")
	if !ok {
		t.Fatal("registered auth not found")
	}

	code, payload, body := requestQuotaHistory(t, h, "")
	if code != http.StatusOK || !payload.Enabled || payload.RetentionHours != 720 || len(payload.Credentials) != 1 {
		t.Fatalf("status=%d body=%s", code, body)
	}
	credential := payload.Credentials[0]
	if credential.ID != "claude-a.json" || credential.Name != "Claude-A.json" || credential.Provider != "claude" {
		t.Fatalf("identity = %+v", credential)
	}
	if want := registered.EnsureIndex(); want == "" || credential.AuthIndex != want {
		t.Fatalf("auth_index = %q, want %q", credential.AuthIndex, want)
	}
	if len(credential.Windows) != 1 {
		t.Fatalf("windows = %+v", credential.Windows)
	}
	window := credential.Windows[0]
	if window.Kind != coreauth.QuotaWindowWeekly || window.Scope != "" || window.PeriodSeconds != 604800 {
		t.Fatalf("window = %+v", window)
	}
	want := [][3]float64{
		{float64(base.Unix()), 0.304, float64(reset.Unix())},
		{float64(base.Add(time.Hour).Unix()), 0.3595, 0},
	}
	if len(window.Points) != len(want) || window.Points[0] != want[0] || window.Points[1] != want[1] {
		t.Fatalf("points = %v, want %v", window.Points, want)
	}
}

func TestGetQuotaHistorySinceParsingAndClamp(t *testing.T) {
	store := coreauth.NewQuotaHistory()
	recordWeeklyQuota(store, "claude-a.json", time.Now().Add(-time.Hour), 0.4, time.Now().Add(24*time.Hour))
	useQuotaHistoryStore(t, store)
	h, _ := newQuotaHistoryHandler(t, &config.Config{AuthDir: t.TempDir()}, &coreauth.Auth{ID: "claude-a.json", Provider: "claude", Status: coreauth.StatusActive})

	since := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	for _, query := range []string{"?since=" + strconv.FormatInt(since.Unix(), 10), "?since=" + since.Format(time.RFC3339)} {
		code, payload, body := requestQuotaHistory(t, h, query)
		if code != http.StatusOK || payload.Since != since.Format(time.RFC3339) {
			t.Fatalf("%s: status=%d body=%s, want since %s", query, code, body, since.Format(time.RFC3339))
		}
	}

	if code, _, body := requestQuotaHistory(t, h, "?since=yesterday"); code != http.StatusBadRequest || body != `{"error":"invalid since"}` {
		t.Fatalf("garbage since: status=%d body=%s", code, body)
	}

	for _, tc := range []struct {
		query  string
		offset time.Duration
	}{
		{"", -14 * 24 * time.Hour},
		{"?since=0", -coreauth.QuotaHistoryRetention},
		{"?since=" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10), 0},
	} {
		code, payload, body := requestQuotaHistory(t, h, tc.query)
		if code != http.StatusOK {
			t.Fatalf("%q: status=%d body=%s", tc.query, code, body)
		}
		now, errNow := time.Parse(time.RFC3339, payload.Now)
		if errNow != nil {
			t.Fatalf("%q: now %q: %v", tc.query, payload.Now, errNow)
		}
		if want := now.Add(tc.offset).Format(time.RFC3339); payload.Since != want {
			t.Fatalf("%q: since = %s, want %s", tc.query, payload.Since, want)
		}
	}
}

func TestGetQuotaHistoryProviderFilter(t *testing.T) {
	observed := time.Now().Add(-time.Hour)
	reset := time.Now().Add(24 * time.Hour)
	store := coreauth.NewQuotaHistory()
	recordWeeklyQuota(store, "claude-a.json", observed, 0.4, reset)
	recordWeeklyQuota(store, "codex-a.json", observed, 0.4, reset)
	useQuotaHistoryStore(t, store)
	h, _ := newQuotaHistoryHandler(t, &config.Config{AuthDir: t.TempDir()},
		&coreauth.Auth{ID: "claude-a.json", Provider: "claude", Status: coreauth.StatusActive},
		&coreauth.Auth{ID: "codex-a.json", Provider: "codex", Status: coreauth.StatusActive},
	)

	if _, all, _ := requestQuotaHistory(t, h, ""); len(all.Credentials) != 2 || all.Credentials[0].ID != "claude-a.json" || all.Credentials[1].ID != "codex-a.json" {
		t.Fatalf("unfiltered credentials = %+v, want both sorted by name", all.Credentials)
	}
	if _, codex, _ := requestQuotaHistory(t, h, "?provider=CODEX"); len(codex.Credentials) != 1 || codex.Credentials[0].ID != "codex-a.json" {
		t.Fatalf("codex credentials = %+v", codex.Credentials)
	}
}

func TestGetQuotaHistoryOmitsUnknownAuths(t *testing.T) {
	store := coreauth.NewQuotaHistory()
	recordWeeklyQuota(store, "deleted.json", time.Now().Add(-time.Hour), 0.4, time.Now().Add(24*time.Hour))
	useQuotaHistoryStore(t, store)
	h, _ := newQuotaHistoryHandler(t, &config.Config{AuthDir: t.TempDir()}, &coreauth.Auth{ID: "no-history.json", Provider: "claude", Status: coreauth.StatusActive})

	code, payload, body := requestQuotaHistory(t, h, "")
	if code != http.StatusOK || !payload.Enabled || payload.Credentials == nil || len(payload.Credentials) != 0 {
		t.Fatalf("status=%d body=%s, want an enabled response with no credentials", code, body)
	}
}
