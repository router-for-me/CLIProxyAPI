package executor

import (
	"net/http"
	"testing"
	"time"
)

// --- agAuthQuotaState cache unit tests ---

func resetQuotaCache() {
	agQuotaCacheMu.Lock()
	defer agQuotaCacheMu.Unlock()
	agQuotaCache = make(map[string]*agAuthQuotaState)
}

func TestAgFreeQuotaInCooldown_FreshAuth(t *testing.T) {
	resetQuotaCache()
	if agFreeQuotaInCooldown("auth-fresh") {
		t.Error("fresh auth should not be in free quota cooldown")
	}
}

func TestAgCreditInCooldown_FreshAuth(t *testing.T) {
	resetQuotaCache()
	if agCreditInCooldown("auth-fresh") {
		t.Error("fresh auth should not be in credit cooldown")
	}
}

func TestAgFreeQuotaInCooldown_EmptyID(t *testing.T) {
	resetQuotaCache()
	if agFreeQuotaInCooldown("") {
		t.Error("empty authID should never be in cooldown")
	}
}

func TestAgCreditInCooldown_EmptyID(t *testing.T) {
	resetQuotaCache()
	if agCreditInCooldown("") {
		t.Error("empty authID should never be in cooldown")
	}
}

func TestAgRecordFreeQuotaFailure_PutsCooldown(t *testing.T) {
	resetQuotaCache()
	agRecordFreeQuotaFailure("auth-A")
	if !agFreeQuotaInCooldown("auth-A") {
		t.Error("auth-A free quota should be in cooldown after failure")
	}
	if agCreditInCooldown("auth-A") {
		t.Error("auth-A credit should not be affected by free quota failure")
	}
}

func TestAgRecordCreditFailure_PutsCooldown(t *testing.T) {
	resetQuotaCache()
	agRecordCreditFailure("auth-A")
	if !agCreditInCooldown("auth-A") {
		t.Error("auth-A credit should be in cooldown after failure")
	}
	if agFreeQuotaInCooldown("auth-A") {
		t.Error("auth-A free quota should not be affected by credit failure")
	}
}

func TestAgResetFreeQuota(t *testing.T) {
	resetQuotaCache()
	agRecordFreeQuotaFailure("auth-A")
	if !agFreeQuotaInCooldown("auth-A") {
		t.Fatal("precondition: should be in cooldown")
	}
	agResetFreeQuota("auth-A")
	if agFreeQuotaInCooldown("auth-A") {
		t.Error("free quota should no longer be in cooldown after reset")
	}
}

func TestAgResetCredit(t *testing.T) {
	resetQuotaCache()
	agRecordCreditFailure("auth-A")
	if !agCreditInCooldown("auth-A") {
		t.Fatal("precondition: should be in cooldown")
	}
	agResetCredit("auth-A")
	if agCreditInCooldown("auth-A") {
		t.Error("credit should no longer be in cooldown after reset")
	}
}

func TestAgResetFreeQuota_EmptyAndUnknown(t *testing.T) {
	resetQuotaCache()
	// Should not panic on empty or unknown auth.
	agResetFreeQuota("")
	agResetFreeQuota("unknown-auth")
}

func TestAgResetCredit_EmptyAndUnknown(t *testing.T) {
	resetQuotaCache()
	agResetCredit("")
	agResetCredit("unknown-auth")
}

func TestAgRecordFreeQuotaFailure_EmptyID(t *testing.T) {
	resetQuotaCache()
	agRecordFreeQuotaFailure("")
	// Should not create entry for empty key.
	agQuotaCacheMu.Lock()
	_, exists := agQuotaCache[""]
	agQuotaCacheMu.Unlock()
	if exists {
		t.Error("should not create cache entry for empty authID")
	}
}

func TestAgRecordCreditFailure_EmptyID(t *testing.T) {
	resetQuotaCache()
	agRecordCreditFailure("")
	agQuotaCacheMu.Lock()
	_, exists := agQuotaCache[""]
	agQuotaCacheMu.Unlock()
	if exists {
		t.Error("should not create cache entry for empty authID")
	}
}

// --- Multi-auth isolation ---

func TestQuotaCooldown_AuthIsolation(t *testing.T) {
	resetQuotaCache()

	agRecordFreeQuotaFailure("auth-A")
	agRecordCreditFailure("auth-B")

	if !agFreeQuotaInCooldown("auth-A") {
		t.Error("auth-A free quota should be in cooldown")
	}
	if agCreditInCooldown("auth-A") {
		t.Error("auth-A credit should NOT be in cooldown")
	}
	if agFreeQuotaInCooldown("auth-B") {
		t.Error("auth-B free quota should NOT be in cooldown")
	}
	if !agCreditInCooldown("auth-B") {
		t.Error("auth-B credit should be in cooldown")
	}
}

// --- Backoff escalation ---

func TestAgNextCooldown_Escalation(t *testing.T) {
	d0, l1 := agNextCooldown(0) // level 0 → 10s
	if d0 != 10*time.Second {
		t.Errorf("level 0: want 10s, got %v", d0)
	}
	d1, l2 := agNextCooldown(l1) // level 1 → 20s
	if d1 != 20*time.Second {
		t.Errorf("level 1: want 20s, got %v", d1)
	}
	d2, _ := agNextCooldown(l2) // level 2 → 40s
	if d2 != 40*time.Second {
		t.Errorf("level 2: want 40s, got %v", d2)
	}
}

func TestAgNextCooldown_CapsAtMax(t *testing.T) {
	// Level 11: 10s * 2^11 = 20480s ≈ 341min > 30min cap.
	level := 11
	d, nextLevel := agNextCooldown(level)
	if d != agQuotaBackoffMax {
		t.Errorf("expected cap at %v, got %v", agQuotaBackoffMax, d)
	}
	if nextLevel != level {
		t.Errorf("level should not increment past cap, got %d", nextLevel)
	}
}

func TestAgNextCooldown_OverflowFallsBackToBase(t *testing.T) {
	// Extremely high level overflows 1<<level to 0; function clamps to base.
	d, _ := agNextCooldown(100)
	if d != agQuotaBackoffBase {
		t.Errorf("overflow level should fall back to base %v, got %v", agQuotaBackoffBase, d)
	}
}

func TestAgNextCooldown_NegativeLevel(t *testing.T) {
	d, next := agNextCooldown(-5)
	if d != agQuotaBackoffBase {
		t.Errorf("negative level should yield base %v, got %v", agQuotaBackoffBase, d)
	}
	if next != 1 {
		t.Errorf("next level should be 1, got %d", next)
	}
}

func TestAgBackoffLevelEscalates_OnRepeatedFailure(t *testing.T) {
	resetQuotaCache()

	agRecordFreeQuotaFailure("auth-esc")
	agQuotaCacheMu.Lock()
	level1 := agQuotaCache["auth-esc"].freeQuotaBackoffLevel
	agQuotaCacheMu.Unlock()

	agRecordFreeQuotaFailure("auth-esc")
	agQuotaCacheMu.Lock()
	level2 := agQuotaCache["auth-esc"].freeQuotaBackoffLevel
	agQuotaCacheMu.Unlock()

	if level2 <= level1 {
		t.Errorf("backoff level should escalate: level1=%d, level2=%d", level1, level2)
	}
}

func TestAgResetFreeQuota_ClearsBackoffLevel(t *testing.T) {
	resetQuotaCache()

	agRecordFreeQuotaFailure("auth-X")
	agRecordFreeQuotaFailure("auth-X") // escalate
	agResetFreeQuota("auth-X")

	agQuotaCacheMu.Lock()
	s := agQuotaCache["auth-X"]
	agQuotaCacheMu.Unlock()

	if s.freeQuotaBackoffLevel != 0 {
		t.Errorf("backoff level should be reset to 0, got %d", s.freeQuotaBackoffLevel)
	}
}

// --- Routing decision tests (simulating executor pre-check logic) ---

// quotaRoutingDecision mirrors the executor's pre-check logic:
//
//	returns "free", "credit", or "blocked".
func quotaRoutingDecision(authID string) string {
	if agFreeQuotaInCooldown(authID) {
		if agCreditInCooldown(authID) {
			return "blocked"
		}
		return "credit"
	}
	return "free"
}

func TestQuotaRouting_FreshAuth_UsesFreeQuota(t *testing.T) {
	resetQuotaCache()
	if d := quotaRoutingDecision("fresh"); d != "free" {
		t.Errorf("fresh auth should route to free, got %s", d)
	}
}

func TestQuotaRouting_FreeExhausted_UsesCredit(t *testing.T) {
	resetQuotaCache()
	agRecordFreeQuotaFailure("auth-1")
	if d := quotaRoutingDecision("auth-1"); d != "credit" {
		t.Errorf("after free quota failure should route to credit, got %s", d)
	}
}

func TestQuotaRouting_BothExhausted_Blocked(t *testing.T) {
	resetQuotaCache()
	agRecordFreeQuotaFailure("auth-1")
	agRecordCreditFailure("auth-1")
	if d := quotaRoutingDecision("auth-1"); d != "blocked" {
		t.Errorf("both exhausted should be blocked, got %s", d)
	}
}

func TestQuotaRouting_FreeExhausted_CreditReset_UsesCredit(t *testing.T) {
	resetQuotaCache()
	agRecordFreeQuotaFailure("auth-1")
	agRecordCreditFailure("auth-1")
	agResetCredit("auth-1") // credit recovered (e.g. cooldown expired)
	if d := quotaRoutingDecision("auth-1"); d != "credit" {
		t.Errorf("after credit reset should route to credit, got %s", d)
	}
}

func TestQuotaRouting_FreeReset_UsesFreeAgain(t *testing.T) {
	resetQuotaCache()
	agRecordFreeQuotaFailure("auth-1")
	agResetFreeQuota("auth-1")
	if d := quotaRoutingDecision("auth-1"); d != "free" {
		t.Errorf("after free reset should route to free, got %s", d)
	}
}

// --- Full multi-auth conductor simulation ---

type routeAttempt struct {
	AuthID  string
	Channel string
}

// simulateConductorRouting simulates the conductor's executeMixedOnce with
// quota-retry second pass, matching the real implementation:
//
//	Pass 1: try each auth's free quota; on 429 record failure, move to next.
//	All 429 → quotaRetryDone=false, lastErr is 429 → clear tried, second pass.
//	Pass 2: same auths; agFreeQuotaInCooldown=true → automatically use credit.
//
// This mirrors the conductor's behavior regardless of request-retry config,
// because the second pass happens within executeMixedOnce (not the outer loop).
func simulateConductorRouting(authIDs []string) []routeAttempt {
	var log []routeAttempt

	allQuota429 := true

	// Pass 1: try each auth's free quota.
	for _, id := range authIDs {
		decision := quotaRoutingDecision(id)
		log = append(log, routeAttempt{id, decision})
		if decision == "free" {
			// Simulate free quota 429 response.
			agRecordFreeQuotaFailure(id)
		} else {
			allQuota429 = false
		}
	}

	// Conductor quota-retry: if all failed with 429 and not yet retried,
	// clear tried map and do a second pass within the same executeMixedOnce.
	if !allQuota429 {
		return log
	}

	for _, id := range authIDs {
		decision := quotaRoutingDecision(id)
		log = append(log, routeAttempt{id, decision})
		if decision == "credit" {
			// Simulate credit 429 response.
			agRecordCreditFailure(id)
		}
	}

	return log
}

func TestConductorRouting_TwoAuths_FreeFirst_ThenCredit(t *testing.T) {
	resetQuotaCache()
	auths := []string{"auth-A", "auth-B"}
	log := simulateConductorRouting(auths)

	// Expected: [A:free, B:free, A:credit, B:credit]
	expected := []routeAttempt{
		{"auth-A", "free"},
		{"auth-B", "free"},
		{"auth-A", "credit"},
		{"auth-B", "credit"},
	}

	if len(log) != len(expected) {
		t.Fatalf("expected %d attempts, got %d: %+v", len(expected), len(log), log)
	}
	for i, want := range expected {
		got := log[i]
		if got.AuthID != want.AuthID || got.Channel != want.Channel {
			t.Errorf("attempt[%d]: want {%s, %s}, got {%s, %s}", i, want.AuthID, want.Channel, got.AuthID, got.Channel)
		}
	}
}

func TestConductorRouting_ThreeAuths_AllFreeBeforeCredit(t *testing.T) {
	resetQuotaCache()
	auths := []string{"auth-A", "auth-B", "auth-C"}
	log := simulateConductorRouting(auths)

	// Pass 1: all free. Pass 2: all credit.
	if len(log) != 6 {
		t.Fatalf("expected 6 attempts, got %d: %+v", len(log), log)
	}
	for i := 0; i < 3; i++ {
		if log[i].Channel != "free" {
			t.Errorf("pass 1 attempt[%d]: expected free, got %s", i, log[i].Channel)
		}
	}
	for i := 3; i < 6; i++ {
		if log[i].Channel != "credit" {
			t.Errorf("pass 2 attempt[%d]: expected credit, got %s", i, log[i].Channel)
		}
	}
}

func TestConductorRouting_SingleAuth_FreesThenCredit(t *testing.T) {
	resetQuotaCache()
	log := simulateConductorRouting([]string{"auth-solo"})

	if len(log) != 2 {
		t.Fatalf("expected 2 attempts, got %d: %+v", len(log), log)
	}
	if log[0].Channel != "free" {
		t.Errorf("attempt[0]: expected free, got %s", log[0].Channel)
	}
	if log[1].Channel != "credit" {
		t.Errorf("attempt[1]: expected credit, got %s", log[1].Channel)
	}
}

func TestConductorRouting_SecondPassBlocked_WhenCreditAlreadyFailed(t *testing.T) {
	resetQuotaCache()
	// Pre-poison auth-A credit cooldown before conductor starts.
	agRecordCreditFailure("auth-A")

	var log []routeAttempt

	// Pass 1: free quota.
	decision1 := quotaRoutingDecision("auth-A")
	log = append(log, routeAttempt{"auth-A", decision1})
	agRecordFreeQuotaFailure("auth-A")

	// Pass 2 (quota retry): both should be in cooldown.
	decision2 := quotaRoutingDecision("auth-A")
	log = append(log, routeAttempt{"auth-A", decision2})

	if log[0].Channel != "free" {
		t.Errorf("pass 1: expected free, got %s", log[0].Channel)
	}
	if log[1].Channel != "blocked" {
		t.Errorf("pass 2: expected blocked, got %s", log[1].Channel)
	}
}

// TestConductorRouting_WorksWithZeroRequestRetry verifies that the quota-retry
// second pass happens within executeMixedOnce, independent of the outer
// request-retry config. Even with request-retry=0, credit fallback works.
func TestConductorRouting_WorksWithZeroRequestRetry(t *testing.T) {
	resetQuotaCache()
	// Simulate request-retry=0: only executeMixedOnce runs (no outer retry loop).
	// The quota-retry second pass within executeMixedOnce handles credit fallback.
	auths := []string{"auth-A", "auth-B"}
	log := simulateConductorRouting(auths)

	// Must still get credit attempts in the second pass.
	if len(log) != 4 {
		t.Fatalf("expected 4 attempts (2 free + 2 credit), got %d: %+v", len(log), log)
	}
	if log[2].Channel != "credit" || log[3].Channel != "credit" {
		t.Errorf("second pass should use credit: got %+v", log[2:])
	}
}

// --- Helper function unit tests ---

func TestAntigravityShouldRetryNoCapacity(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       bool
	}{
		{"503 no capacity", http.StatusServiceUnavailable, "no capacity available", true},
		{"503 other error", http.StatusServiceUnavailable, "internal error", false},
		{"429 not no-capacity", http.StatusTooManyRequests, "rate limit", false},
		{"200 success", http.StatusOK, "ok", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := antigravityShouldRetryNoCapacity(tt.statusCode, []byte(tt.body))
			if got != tt.want {
				t.Errorf("antigravityShouldRetryNoCapacity(%d, %q) = %v, want %v",
					tt.statusCode, tt.body, got, tt.want)
			}
		})
	}
}

func TestAntigravityNoCapacityRetryDelay(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 250 * time.Millisecond},
		{1, 500 * time.Millisecond},
		{2, 750 * time.Millisecond},
		{7, 2 * time.Second}, // capped
		{-1, 250 * time.Millisecond},
	}
	for _, tt := range tests {
		got := antigravityNoCapacityRetryDelay(tt.attempt)
		if got != tt.want {
			t.Errorf("antigravityNoCapacityRetryDelay(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestAntigravityIsFreeQuotaExhausted_CaseSensitivity(t *testing.T) {
	// The function lowercases before checking — verify mixed case works.
	tests := []struct {
		body string
		want bool
	}{
		{`{"error":{"status":"RESOURCE_EXHAUSTED"}}`, true},
		{`{"error":{"status":"resource_exhausted"}}`, true},
		{`{"error":{"message":"Quota limit reached"}}`, true},
		{`{"error":{"message":"QUOTA exceeded"}}`, true},
	}
	for _, tt := range tests {
		got := antigravityIsFreeQuotaExhausted(http.StatusTooManyRequests, []byte(tt.body))
		if got != tt.want {
			t.Errorf("antigravityIsFreeQuotaExhausted(429, %q) = %v, want %v", tt.body, got, tt.want)
		}
	}
}
