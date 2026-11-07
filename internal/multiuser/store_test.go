package multiuser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func testAccounts(t *testing.T) (*Store, User, User) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "中文账户"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	admin, err := s.Register("alice", "password-alice", "")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := s.CreateInvite(admin, 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.Register("bob", "password-bob", invite.Code)
	if err != nil {
		t.Fatal(err)
	}
	return s, admin, bob
}

func TestAccountsMultipleKeysSessionsAndRestart(t *testing.T) {
	s, admin, bob := testAccounts(t)
	if admin.Role != "admin" || bob.Role != "member" {
		t.Fatalf("roles: %s %s", admin.Role, bob.Role)
	}
	if _, err := s.Register("carol", "password-carol", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("registration without invite: %v", err)
	}
	first, secret, err := s.CreateKey(bob, "手机")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.CreateKey(bob, "电脑")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || len(s.Keys(bob)) != 2 || len(s.Keys(admin)) != 0 {
		t.Fatal("keys are not per-user independent objects")
	}
	provider := &AccessProvider{Store: s}
	request := httptest.NewRequest("GET", "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	result, errAuth := provider.Authenticate(context.Background(), request)
	if errAuth != nil || result.Principal != first.ID || result.Metadata["username"] != "bob" {
		t.Fatalf("provider identity: %#v %v", result, errAuth)
	}
	if result.Metadata["key_auto_balance"] != "false" {
		t.Fatalf("auto balance metadata = %v, want false", result.Metadata["key_auto_balance"])
	}
	enable := true
	if err := s.UpdateKey(bob, first.ID, nil, nil, &enable, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, key, ok := s.AuthenticateKey(secret); !ok || !key.AutoBalance {
		t.Fatal("auto balance flag not applied to the key")
	}
	if result, errAuth = provider.Authenticate(context.Background(), request); errAuth != nil || result.Metadata["key_auto_balance"] != "true" {
		t.Fatalf("auto balance metadata: %#v %v", result, errAuth)
	}
	_, sessionToken, err := s.Login(bob.Username, "password-bob")
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if _, ok := reopened.AuthenticateSession(sessionToken); !ok {
		t.Fatal("session did not survive restart")
	}
	if _, key, ok := reopened.AuthenticateKey(secret); !ok || !key.AutoBalance {
		t.Fatal("key or its auto balance flag did not survive restart")
	}
	listed := reopened.Keys(bob)
	if len(listed) != 2 || listed[0].Secret == "" || listed[1].Secret == "" {
		t.Fatal("stored keys must keep their secret so owners can copy them again")
	}
	for _, key := range listed {
		if key.ID == first.ID && key.Secret != secret {
			t.Fatal("listed key secret does not match the one returned at creation")
		}
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), sessionToken) || strings.Contains(string(raw), "password-bob") {
		t.Fatal("account store contains plaintext session or password")
	}
	if err := s.UpdateKey(admin, first.ID, nil, nil, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.AuthenticateKey(secret); ok {
		t.Fatal("revoked key still authenticates")
	}
	if len(s.Keys(bob)) != 1 {
		t.Fatal("revoked key is still listed")
	}
	disabled := true
	if err := s.UpdateUser(admin, bob.Username, &disabled, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.AuthenticateSession(sessionToken); ok {
		t.Fatal("disabled user session still authenticates")
	}
	if _, _, err := s.Login(bob.Username, "password-bob"); !errors.Is(err, ErrDenied) {
		t.Fatal("disabled user can log in")
	}
	if err := s.UpdateUser(admin, admin.Username, &disabled, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("last administrator protection: %v", err)
	}
}

func TestAccountAndChannelIdentifiersIgnoreStoredLabels(t *testing.T) {
	s, admin, _ := testAccounts(t)
	ch, err := s.CreateChannel(admin, "573", false)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dir, "accounts.db")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]map[string]map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	stored["users"][admin.Username]["display_name"] = "旧昵称"
	stored["channels"][ch.ID]["name"] = "旧渠道名称"
	raw, err = json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	user, _, err := reopened.Login(admin.Username, "password-alice")
	if err != nil || user.Username != admin.Username {
		t.Fatalf("stored display name changed login identity: %#v %v", user, err)
	}
	loaded, ok := reopened.Channel(ch.ID)
	if !ok || loaded.Slug != ch.Slug || loaded.Prefix != ch.Prefix {
		t.Fatalf("stored name changed channel identity: %#v", loaded)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "display_name") || strings.Contains(string(raw), `"name"`) {
		t.Fatal("account store retained separate identity labels after saving")
	}
}

func TestInvitesConsumeAtomically(t *testing.T) {
	s, admin, bob := testAccounts(t)
	if _, err := s.CreateInvite(bob, 1, 7); !errors.Is(err, ErrDenied) {
		t.Fatal("member can create invite")
	}
	invite, err := s.CreateInvite(admin, 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"carol", "david"} {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Register(name, "password-test", invite.Code); results <- err }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("single-use invite admitted %d users", success)
	}
}

func TestSessionAndInviteExpirationWithoutSleeping(t *testing.T) {
	s, admin, _ := testAccounts(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	_, token, err := s.Login(admin.Username, "password-alice")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := s.CreateInvite(admin, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Hour)
	if _, ok := s.AuthenticateSession(token); ok {
		t.Fatal("expired session authenticates")
	}
	if _, err := s.Register("carol", "password-carol", invite.Code); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expired invite accepted: %v", err)
	}
}

func TestChannelsAndKeysEnforceOwnerWithAdminOverride(t *testing.T) {
	s, admin, bob := testAccounts(t)
	ch, err := s.CreateChannel(bob, "gpt", false)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Prefix != "gpt" {
		t.Fatalf("prefix %s", ch.Prefix)
	}
	if _, err := s.CreateChannel(bob, "gpt", false); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate channel allowed")
	}
	if _, err := s.CreateChannel(admin, "gpt", false); !errors.Is(err, ErrConflict) {
		t.Fatal("channel slug not unique site-wide")
	}
	if _, err := s.CreateChannel(bob, "gpt/model", false); !errors.Is(err, ErrInvalid) {
		t.Fatal("nested prefix allowed")
	}
	if _, err := s.UpdateChannel(User{Username: "carol", Role: "admin"}, ch.ID, nil, nil, nil); !errors.Is(err, ErrDenied) {
		t.Fatal("caller-supplied role bypassed authorization")
	}
	published := false
	if _, err := s.UpdateChannel(admin, ch.ID, &published, nil, nil); err != nil {
		t.Fatal(err)
	}
	key, _, err := s.CreateKey(admin, "admin key")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateKey(bob, key.ID, nil, nil, nil, nil, true); !errors.Is(err, ErrDenied) {
		t.Fatal("member revoked another user's key")
	}
}

func TestUsageAttributionRetryTotalsAndPersistence(t *testing.T) {
	s, _, _ := testAccounts(t)
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	base := usage.Record{Attribution: usage.Attribution{SiteID: s.SiteID(), Consumer: "bob", KeyID: "key-one", KeyName: "手机", Owner: "alice", ChannelID: "ch-one", Channel: "alice-gpt", CredentialName: "cred.json"}, RequestedAt: at, AuthID: "cred.json", Alias: "alice-gpt/gpt-5", Model: "gpt-5", TraceID: "trace-one", RequestID: "attempt-one", ReasoningEffort: "high", Failed: true, Fail: usage.Failure{StatusCode: 429}}
	s.HandleUsage(context.Background(), base)
	base.RequestID = "attempt-two"
	base.Failed = false
	base.Detail = usage.Detail{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}
	s.HandleUsage(context.Background(), base)
	base.Attribution.Consumer = "carol"
	base.TraceID = "trace-two"
	base.RequestID = "attempt-three"
	s.HandleUsage(context.Background(), base)
	base.Attribution.SiteID = "some-other-server"
	s.HandleUsage(context.Background(), base)
	reopened, err := Open(s.dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	total, groups, err := reopened.Summary(Filter{Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if total.Calls != 2 || total.Executions != 3 || total.TotalTokens != 60 || total.Failures != 1 || len(groups) != 2 {
		t.Fatalf("wrong retry totals: %#v %#v", total, groups)
	}
	items, count, err := reopened.Usage(Filter{Consumer: "bob", Owner: "alice", KeyID: "key-one"})
	if err != nil || count != 2 || len(items) != 2 || items[0].KeyName != "手机" {
		t.Fatalf("wrong usage filter: %#v %d %v", items, count, err)
	}
	if items[0].ReasoningEffort != "high" || items[1].ReasoningEffort != "high" {
		t.Fatalf("reasoning effort not persisted: %#v", items)
	}
	items, _, err = reopened.Usage(Filter{From: at.Add(time.Second)})
	if err != nil || len(items) != 0 {
		t.Fatal("time filter ignored")
	}
	if err := reopened.Audit(AuditEvent{Actor: "bob", Action: "credential.update", Target: "cred.json", Status: 200}); err != nil {
		t.Fatal(err)
	}
	audits, _, err := reopened.Audits(Filter{Actor: "bob", Action: "credential"})
	if err != nil || len(audits) != 1 {
		t.Fatal("audit persistence failed")
	}
}

func TestSummaryLatencyAndHourlyBuckets(t *testing.T) {
	s, _, _ := testAccounts(t)
	at := time.Date(2026, 10, 6, 12, 10, 0, 0, time.UTC)
	record := func(trace string, at time.Time, failed bool, latency, ttft time.Duration, tokens int64) usage.Record {
		return usage.Record{Attribution: usage.Attribution{SiteID: s.SiteID(), Consumer: "bob", Owner: "alice", Channel: "alice-gpt"}, RequestedAt: at, AuthID: "cred.json", Alias: "alice-gpt/gpt-5", Model: "gpt-5", TraceID: trace, RequestID: trace, Failed: failed, Latency: latency, TTFT: ttft, Detail: usage.Detail{InputTokens: tokens, TotalTokens: tokens}}
	}
	s.HandleUsage(context.Background(), record("t1", at, false, 2*time.Second, 400*time.Millisecond, 10))
	s.HandleUsage(context.Background(), record("t2", at.Add(20*time.Minute), false, 4*time.Second, 0, 20))
	// Failed attempts never contribute latency, so slow errors cannot skew averages.
	s.HandleUsage(context.Background(), record("t3", at.Add(time.Hour), true, 30*time.Second, time.Second, 0))
	result, err := s.SummaryResultContext(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	total := result.Total
	if total.LatencyMS != 6000 || total.LatencyCount != 2 || total.TTFTMS != 400 || total.TTFTCount != 1 {
		t.Fatalf("wrong latency aggregates: %#v", total)
	}
	if len(result.Buckets) != 2 {
		t.Fatalf("expected two hourly buckets: %#v", result.Buckets)
	}
	first, second := result.Buckets[0], result.Buckets[1]
	if first.Start != at.Truncate(time.Hour).Unix() || first.Calls != 2 || first.TotalTokens != 30 || first.Failures != 0 {
		t.Fatalf("wrong first bucket: %#v", first)
	}
	if second.Start != at.Truncate(time.Hour).Add(time.Hour).Unix() || second.Calls != 1 || second.Failures != 1 {
		t.Fatalf("wrong second bucket: %#v", second)
	}
}

func TestModelPerformanceBucketsLast24Hours(t *testing.T) {
	s, _, _ := testAccounts(t)
	now := time.Date(2026, 10, 6, 12, 30, 0, 0, time.UTC)
	record := func(at time.Time, failed bool, latency, ttft time.Duration, output int64) usage.Record {
		return usage.Record{
			Attribution: usage.Attribution{SiteID: s.SiteID(), Consumer: "bob", Owner: "alice", ChannelID: "ch-one", Channel: "alice-gpt"},
			RequestedAt: at, AuthID: "cred.json", Alias: "alice-gpt/gpt-5", Model: "gpt-5",
			Failed: failed, Latency: latency, TTFT: ttft,
			Detail: usage.Detail{OutputTokens: output},
		}
	}
	s.HandleUsage(context.Background(), record(now.Add(-2*time.Hour), false, 400*time.Millisecond, 100*time.Millisecond, 300))
	s.HandleUsage(context.Background(), record(now.Add(-2*time.Hour), true, 50*time.Millisecond, 0, 0))
	s.HandleUsage(context.Background(), record(now.Add(-time.Hour), false, 200*time.Millisecond, 0, 100))
	// Outside the 24-hour window and another model: must be ignored.
	s.HandleUsage(context.Background(), record(now.Add(-26*time.Hour), false, 900*time.Millisecond, 0, 900))
	other := record(now.Add(-time.Hour), false, 100*time.Millisecond, 0, 50)
	other.Alias = "alice-gpt/gpt-4"
	s.HandleUsage(context.Background(), other)

	perf, err := s.ModelPerformance(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(perf) != 2 {
		t.Fatalf("expected 2 models, got %d", len(perf))
	}
	p := perf["alice-gpt/gpt-5"]
	if p == nil || len(p.Buckets) != 24 {
		t.Fatalf("missing model or buckets: %#v", p)
	}
	// Failures count toward requests and the rate, but not latency/TPS sums.
	if p.Totals.Requests != 3 || p.Totals.Failures != 1 {
		t.Fatalf("wrong totals: %#v", p.Totals)
	}
	if p.Totals.LatencyCount != 2 || p.Totals.LatencyMS != 600 {
		t.Fatalf("wrong latency sums: %#v", p.Totals)
	}
	if p.Totals.TTFTCount != 1 || p.Totals.TTFTMS != 100 {
		t.Fatalf("wrong ttft sums: %#v", p.Totals)
	}
	// Generation time: 400-100 for the streamed call, full 200 for the rest.
	if p.Totals.GenerationMS != 500 || p.Totals.OutputTokens != 400 {
		t.Fatalf("wrong generation sums: %#v", p.Totals)
	}
	filled := 0
	for _, b := range p.Buckets {
		if b.Requests > 0 {
			filled++
			if b.Start == 0 {
				t.Fatal("bucket start not initialized")
			}
		}
	}
	if filled != 2 {
		t.Fatalf("expected 2 filled buckets, got %d", filled)
	}
}

func TestFailedPersistenceDoesNotConsumeInvite(t *testing.T) {
	s, admin, _ := testAccounts(t)
	invite, err := s.CreateInvite(admin, 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	original := s.dir
	s.dir = filepath.Join(original, "missing-directory")
	if _, err := s.Register("carol", "password-carol", invite.Code); err == nil {
		t.Fatal("expected persistence error")
	}
	if _, ok := s.User("carol"); ok {
		t.Fatal("failed mutation published partial account state")
	}
	s.dir = original
	if _, err := s.Register("carol", "password-carol", invite.Code); err != nil {
		t.Fatalf("failed save consumed invite: %v", err)
	}
}

func TestCredentialIndexAllocationIsMonotonicPerType(t *testing.T) {
	s, admin, _ := testAccounts(t)
	ch, err := s.CreateChannel(admin, "573", false)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.AllocateCredentialIndex(ch.ID, "kimi")
	if err != nil || first != 1 {
		t.Fatalf("first index %d %v", first, err)
	}
	second, err := s.AllocateCredentialIndex(ch.ID, "kimi")
	if err != nil || second != 2 {
		t.Fatalf("second index %d %v", second, err)
	}
	claude, err := s.AllocateCredentialIndex(ch.ID, "claude")
	if err != nil || claude != 1 {
		t.Fatalf("claude index %d %v", claude, err)
	}
	if err := s.SetCredentialIndexFloor(ch.ID, "kimi", 7); err != nil {
		t.Fatal(err)
	}
	next, err := s.AllocateCredentialIndex(ch.ID, "kimi")
	if err != nil || next != 8 {
		t.Fatalf("floor not respected: %d %v", next, err)
	}
	if err := s.SetCredentialIndexFloor(ch.ID, "kimi", 3); err != nil {
		t.Fatal(err)
	}
	again, err := s.AllocateCredentialIndex(ch.ID, "kimi")
	if err != nil || again != 9 {
		t.Fatalf("floor lowered the counter: %d %v", again, err)
	}
	if _, err := s.AllocateCredentialIndex(ch.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty credential type allocated")
	}
	if _, err := s.AllocateCredentialIndex("ch-missing", "kimi"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing channel allocated")
	}
}

func TestChannelAnnouncementAndPublicPermissions(t *testing.T) {
	s, admin, bob := testAccounts(t)
	ch, err := s.CreateChannel(admin, "573", false)
	if err != nil {
		t.Fatal(err)
	}
	announcement := "本周六维护"
	if _, err := s.UpdateChannel(bob, ch.ID, nil, &announcement, nil); !errors.Is(err, ErrDenied) {
		t.Fatal("member edited a private channel")
	}
	updated, err := s.UpdateChannel(admin, ch.ID, nil, &announcement, nil)
	if err != nil || updated.Announcement != announcement || updated.AnnouncementUpdatedAt.IsZero() {
		t.Fatalf("announcement: %#v %v", updated, err)
	}
	tooLong := strings.Repeat("字", maxAnnouncementRunes+1)
	if _, err := s.UpdateChannel(admin, ch.ID, nil, &tooLong, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversized announcement accepted")
	}
	public := true
	if _, err := s.UpdateChannel(bob, ch.ID, nil, nil, &public); !errors.Is(err, ErrDenied) {
		t.Fatal("member toggled the public flag")
	}
	if _, err := s.UpdateChannel(admin, ch.ID, nil, nil, &public); err != nil {
		t.Fatal(err)
	}
	if !s.CanManageChannel(bob, ch.ID) {
		t.Fatal("public channel not manageable by members")
	}
	announcement = "成员维护公告"
	updated, err = s.UpdateChannel(bob, ch.ID, nil, &announcement, nil)
	if err != nil || updated.Announcement != announcement || updated.Slug != ch.Slug || updated.Prefix != ch.Prefix {
		t.Fatalf("public channel update changed its identity: %#v %v", updated, err)
	}
	private := false
	if _, err := s.UpdateChannel(bob, ch.ID, nil, nil, &private); !errors.Is(err, ErrDenied) {
		t.Fatal("member locked a public channel back to private")
	}
}

func TestDeleteChannelPermissionsAndCascade(t *testing.T) {
	s, admin, bob := testAccounts(t)
	ch, err := s.CreateChannel(bob, "pool", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddBoardMessage(bob, ch.ID, "公告：周末维护"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteChannel(bob, ch.ID+"-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing channel deletion did not report not-found")
	}
	adminCh, err := s.CreateChannel(admin, "admin-pool", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteChannel(bob, adminCh.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("member deleted another user's channel")
	}
	if err := s.DeleteChannel(admin, ch.ID); err != nil {
		t.Fatalf("admin override failed: %v", err)
	}
	if _, ok := s.Channel(ch.ID); ok {
		t.Fatal("deleted channel still listed")
	}
	if len(s.ListBoardMessages(ch.ID, 0, "")) != 0 {
		t.Fatal("board messages survived channel deletion")
	}
	second, err := s.CreateChannel(bob, "pool-2", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteChannel(bob, second.ID); err != nil {
		t.Fatalf("owner could not delete: %v", err)
	}
}

func TestBoardMessagesPagingCapAndDeletePermissions(t *testing.T) {
	s, admin, bob := testAccounts(t)
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	private, err := s.CreateChannel(admin, "573", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddBoardMessage(bob, private.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty message accepted")
	}
	first, err := s.AddBoardMessage(bob, private.ID, "kimi#1 今天额度还剩多少？")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	second, err := s.AddBoardMessage(admin, private.ID, "还有 80%，随便用")
	if err != nil {
		t.Fatal(err)
	}
	messages := s.ListBoardMessages(private.ID, 0, "")
	if len(messages) != 2 || messages[0].ID != second.ID || messages[1].ID != first.ID {
		t.Fatalf("messages not newest-first: %#v", messages)
	}
	older := s.ListBoardMessages(private.ID, 0, second.ID)
	if len(older) != 1 || older[0].ID != first.ID {
		t.Fatalf("cursor paging broken: %#v", older)
	}
	if gone := s.ListBoardMessages(private.ID, 0, "msg-missing"); len(gone) != 0 {
		t.Fatal("unknown cursor did not empty the page")
	}
	if err := s.DeleteBoardMessage(admin, private.ID, first.ID); err != nil {
		// admin is also the channel owner here, so deletion must succeed
		t.Fatalf("owner could not moderate: %v", err)
	}
	if err := s.DeleteBoardMessage(bob, private.ID, second.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("member deleted another user's message in a private channel")
	}
	if err := s.DeleteBoardMessage(admin, private.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.ListBoardMessages(private.ID, 0, "")) != 0 {
		t.Fatal("deleted messages still listed")
	}

	publicCh, err := s.CreateChannel(admin, "openpool", true)
	if err != nil {
		t.Fatal(err)
	}
	byBob, err := s.AddBoardMessage(bob, publicCh.ID, "我挂了 kimi#2 上去")
	if err != nil {
		t.Fatal(err)
	}
	byAdmin, err := s.AddBoardMessage(admin, publicCh.ID, "收到")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBoardMessage(bob, publicCh.ID, byAdmin.ID); err != nil {
		t.Fatal("member could not moderate a public channel board")
	}
	if err := s.DeleteBoardMessage(bob, publicCh.ID, byBob.ID); err != nil {
		t.Fatal("author could not delete own message")
	}

	capped, err := s.CreateChannel(admin, "busy", false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < boardMessagesPerChan+25; i++ {
		if _, err := s.AddBoardMessage(bob, capped.ID, "消息"); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.RLock()
	stored := 0
	for _, m := range s.data.Messages {
		if m.ChannelID == capped.ID {
			stored++
		}
	}
	s.mu.RUnlock()
	if stored != boardMessagesPerChan {
		t.Fatalf("board cap not enforced: %d", stored)
	}
}

func TestMatchUsageModelAndKeyQueries(t *testing.T) {
	bare := UsageEvent{Model: "deepseek-flash", Channel: "ch1", KeyID: "phone-key", Consumer: "alice"}
	routed := UsageEvent{Model: "ch1/kimi-k3", Channel: "ch1", KeyID: "work-key", Consumer: "bob"}
	other := UsageEvent{Model: "deepseek-flash", Channel: "ch2", KeyID: "backup-key", Consumer: "alice"}

	cases := []struct {
		name  string
		event UsageEvent
		model string
		keyID string
		want  bool
	}{
		{"namespaced model serving pin", UsageEvent{Model: "vanchin/deepseek-v4.1-flash", Channel: "ch1"}, "vanchin/deepseek-v4.1-flash（ch1/vanchin/deepseek-v4.1-flash）", "", true},
		{"namespaced model other channel", UsageEvent{Model: "vanchin/deepseek-v4.1-flash", Channel: "ch2"}, "vanchin/deepseek-v4.1-flash（ch1/vanchin/deepseek-v4.1-flash）", "", false},
		{"namespaced pinned model", UsageEvent{Model: "ch1/vanchin/deepseek-v4.1-flash", Channel: "ch1"}, "vanchin/deepseek-v4.1-flash（ch1/vanchin/deepseek-v4.1-flash）", "", true},
		{"existing model substring", bare, "deepseek", "", true},
		{"channel text does not add a list filter", bare, "ch1", "", false},
		{"serving substring does not add a list filter", bare, "ch1/deepseek", "", false},
		{"full-width pin matches", bare, "deepseek-flash（ch1/deepseek-flash）", "", true},
		{"ascii pin matches", bare, "deepseek-flash(ch1/deepseek-flash)", "", true},
		{"pin on other channel misses", other, "deepseek-flash（ch1/deepseek-flash）", "", false},
		{"pin hits routed model id", routed, "kimi（ch1/kimi-k3）", "", true},
		{"paren without slash stays literal substring", bare, "deepseek（flash）", "", false},
		{"routed model keeps substring match", routed, "kimi-k3", "", true},
		{"key id exact", bare, "", "phone-key", true},
		{"key id prefix does not match", bare, "", "phone", false},
		{"other key id does not match", bare, "", "work-key", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchUsage(tc.event, Filter{Model: tc.model, KeyID: tc.keyID})
			if got != tc.want {
				t.Fatalf("matchUsage(model=%q keyID=%q) = %v, want %v", tc.model, tc.keyID, got, tc.want)
			}
		})
	}
	if !matchUsage(bare, Filter{Consumer: "alice"}) || !matchUsage(other, Filter{Consumer: "alice"}) || matchUsage(routed, Filter{Consumer: "alice"}) {
		t.Fatal("consumer selection must match all and only that member's keys")
	}
}
