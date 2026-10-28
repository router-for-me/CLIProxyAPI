package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// claudeQuotaAuth builds a Claude credential whose last response reported the given
// weekly (7d) and five-hour utilization (0-1) and resets.
func claudeQuotaAuth(id string, weeklyUsed float64, weeklyReset time.Duration, fiveHourUsed float64) *Auth {
	now := time.Now()
	return &Auth{ID: id, Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Utilization": strconv.FormatFloat(weeklyUsed, 'f', -1, 64),
		"Anthropic-Ratelimit-Unified-7d-Reset":       strconv.FormatInt(now.Add(weeklyReset).Unix(), 10),
		"Anthropic-Ratelimit-Unified-5h-Utilization": strconv.FormatFloat(fiveHourUsed, 'f', -1, 64),
		"Anthropic-Ratelimit-Unified-5h-Reset":       strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10),
	}}}
}

func TestConsumeFirstSelectorRanksBySoonestWeeklyReset(t *testing.T) {
	t.Parallel()
	day := 24 * time.Hour
	now := time.Now()

	tests := []struct {
		name  string
		auths []*Auth
		want  string
	}{
		{
			name:  "sooner weekly reset wins over more headroom",
			auths: []*Auth{claudeQuotaAuth("a-late", 0.2, 5*day, 0.1), claudeQuotaAuth("b-soon", 0.6, day, 0.1)},
			want:  "b-soon",
		},
		{
			name:  "spent weekly window sorts last",
			auths: []*Auth{claudeQuotaAuth("a-late", 0.2, 5*day, 0.1), claudeQuotaAuth("b-spent", 0.97, day, 0.1)},
			want:  "a-late",
		},
		{
			name:  "spent five-hour window sorts last",
			auths: []*Auth{claudeQuotaAuth("a-late", 0.2, 5*day, 0.1), claudeQuotaAuth("b-busy", 0.5, day, 0.96)},
			want:  "a-late",
		},
		{
			name:  "unknown reset sorts after known reset",
			auths: []*Auth{{ID: "a-unknown", Provider: "claude"}, claudeQuotaAuth("b-known", 0.5, 6*day, 0.1)},
			want:  "b-known",
		},
		{
			name:  "window that has not started counts as unknown",
			auths: []*Auth{claudeQuotaAuth("a-unstarted", 0, 7*day, 0), claudeQuotaAuth("b-known", 0.5, 6*day, 0.1)},
			want:  "b-known",
		},
		{
			name:  "reset already passed counts as unknown",
			auths: []*Auth{claudeQuotaAuth("a-stale", 0.5, -time.Hour, 0.1), claudeQuotaAuth("b-known", 0.5, 6*day, 0.1)},
			want:  "b-known",
		},
		{
			name:  "no reset data anywhere falls back to ID order",
			auths: []*Auth{{ID: "b", Provider: "gemini"}, {ID: "a", Provider: "gemini"}},
			want:  "a",
		},
		{
			name: "codex weekly reset relative to the observation",
			auths: []*Auth{
				{ID: "a-codex-late", Provider: "codex", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
					"X-Codex-Primary-Used-Percent": "10", "X-Codex-Primary-Window-Minutes": "10080",
					"X-Codex-Primary-Reset-After-Seconds": strconv.Itoa(int((5 * day).Seconds())),
				}}},
				{ID: "b-codex-soon", Provider: "codex", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
					"X-Codex-Primary-Used-Percent": "40", "X-Codex-Primary-Window-Minutes": "300",
					"X-Codex-Primary-Reset-At":       strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
					"X-Codex-Secondary-Used-Percent": "70", "X-Codex-Secondary-Window-Minutes": "10080",
					"X-Codex-Secondary-Reset-At": strconv.FormatInt(now.Add(2*day).Unix(), 10),
				}}},
			},
			want: "b-codex-soon",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, errPick := (&ConsumeFirstSelector{}).Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, tt.auths)
			if errPick != nil {
				t.Fatalf("Pick() error = %v", errPick)
			}
			if got.ID != tt.want {
				t.Fatalf("Pick() = %q, want %q", got.ID, tt.want)
			}
		})
	}
}

func TestConsumeFirstSelectorKeepsSessionOnItsAccount(t *testing.T) {
	t.Parallel()
	day := 24 * time.Hour
	selector := NewSessionAffinitySelector(&ConsumeFirstSelector{})
	defer selector.Stop()
	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_ac980658-63bd-4fb3-97ba-8da64cb1e344"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	first, errPick := selector.Pick(context.Background(), "claude", "claude-x", opts, []*Auth{
		claudeQuotaAuth("a", 0.5, 2*day, 0.1), claudeQuotaAuth("b", 0.5, 4*day, 0.1),
	})
	if errPick != nil || first.ID != "a" {
		t.Fatalf("first Pick() = %v, %v; want a", first, errPick)
	}
	// b now resets sooner, but the bound session must stay on a for its cache.
	again, errPick := selector.Pick(context.Background(), "claude", "claude-x", opts, []*Auth{
		claudeQuotaAuth("a", 0.6, 2*day, 0.1), claudeQuotaAuth("b", 0.5, day, 0.1),
	})
	if errPick != nil || again.ID != "a" {
		t.Fatalf("second Pick() = %v, %v; want the bound account a", again, errPick)
	}
}

func TestAccountPinServesOrRefusesNamedAccount(t *testing.T) {
	const model = "claude-pin-test-model"
	now := time.Now()
	team := &Auth{ID: "pin-team", Provider: "claude", Metadata: map[string]any{MetadataGroup: "team, pro", "email": "team@example.com"}}
	held := &Auth{ID: "pin-held", Provider: "claude", Metadata: map[string]any{MetadataHoldUntil: now.Add(3 * time.Hour).UTC().Format(time.RFC3339)}}
	cooling := &Auth{ID: "pin-cooling", Provider: "claude", Unavailable: true, NextRetryAfter: now.Add(time.Hour),
		Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Hour)}}

	m := NewManager(nil, &ConsumeFirstSelector{}, nil)
	m.RegisterExecutor(&authFallbackExecutor{id: "claude"})
	reg := registry.GetGlobalRegistry()
	for _, auth := range []*Auth{team, held, cooling} {
		reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}
	execute := func(pin string) (string, error) {
		opts := cliproxyexecutor.Options{}
		if pin != "" {
			opts.Metadata = map[string]any{cliproxyexecutor.AccountPinMetadataKey: pin}
		}
		resp, errExec := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, opts)
		return string(resp.Payload), errExec
	}

	for _, pin := range []string{"", "TEAM", "pro", "team@example.com", "pin-team"} {
		if got, errExec := execute(pin); errExec != nil || got != team.ID {
			t.Fatalf("Execute(pin=%q) = %q, %v; want %q (held and cooling accounts are skipped)", pin, got, errExec, team.ID)
		}
	}

	for _, tc := range []struct {
		pin, wantText string
		wantStatus    int
	}{
		{pin: "pin-held", wantText: "held until", wantStatus: http.StatusTooManyRequests},
		{pin: "pin-cooling", wantText: "at its limit until", wantStatus: http.StatusTooManyRequests},
		{pin: "nobody", wantText: "no credential matches account", wantStatus: http.StatusBadRequest},
	} {
		_, errExec := execute(tc.pin)
		var refusal *accountPinError
		if !errors.As(errExec, &refusal) {
			t.Fatalf("Execute(pin=%q) error = %v, want an account pin refusal", tc.pin, errExec)
		}
		if refusal.StatusCode() != tc.wantStatus || !strings.Contains(refusal.Error(), tc.wantText) {
			t.Fatalf("Execute(pin=%q) = %d %q, want %d containing %q", tc.pin, refusal.StatusCode(), refusal.Error(), tc.wantStatus, tc.wantText)
		}
		if tc.wantStatus == http.StatusTooManyRequests && refusal.Headers().Get("Retry-After") == "" {
			t.Fatalf("Execute(pin=%q) refusal has no Retry-After header", tc.pin)
		}
	}
}

func TestPinOnlyAccountServesPinsAndNeverUnpinnedTraffic(t *testing.T) {
	const model = "claude-pin-only-test-model"
	const day = 24 * time.Hour
	now := time.Now()
	// The pin-only account resets soonest, so consume-first would pick it first.
	team := claudeQuotaAuth("pin-only-team", 0.1, day, 0.1)
	team.Metadata = map[string]any{MetadataGroup: "team", MetadataPinOnly: true}
	pro := claudeQuotaAuth("pin-only-pro", 0.1, 5*day, 0.1)

	m := NewManager(nil, &ConsumeFirstSelector{}, nil)
	m.RegisterExecutor(&authFallbackExecutor{id: "claude"})
	reg := registry.GetGlobalRegistry()
	for _, auth := range []*Auth{team, pro} {
		reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}
	execute := func(pin string) (string, error) {
		opts := cliproxyexecutor.Options{}
		if pin != "" {
			opts.Metadata = map[string]any{cliproxyexecutor.AccountPinMetadataKey: pin}
		}
		resp, errExec := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, opts)
		return string(resp.Payload), errExec
	}

	if got, errExec := execute(""); errExec != nil || got != pro.ID {
		t.Fatalf("unpinned Execute = %q, %v; want %q (pin-only accounts are skipped)", got, errExec, pro.ID)
	}
	if got, errExec := execute("team"); errExec != nil || got != team.ID {
		t.Fatalf("Execute(pin=team) = %q, %v; want %q", got, errExec, team.ID)
	}

	// With every other account unusable, unpinned traffic is refused rather than
	// falling through to the pin-only account.
	pro.Disabled = true
	if _, errUpdate := m.Update(context.Background(), pro); errUpdate != nil {
		t.Fatalf("update pro: %v", errUpdate)
	}
	if got, errExec := execute(""); errExec == nil {
		t.Fatalf("unpinned Execute = %q with only a pin-only account left; want an error", got)
	}
	if got, errExec := execute("team"); errExec != nil || got != team.ID {
		t.Fatalf("Execute(pin=team) = %q, %v; want %q", got, errExec, team.ID)
	}

	// A hold still applies to a pin-only account.
	team.Metadata[MetadataHoldUntil] = now.Add(time.Hour).UTC().Format(time.RFC3339)
	if _, errUpdate := m.Update(context.Background(), team); errUpdate != nil {
		t.Fatalf("update team: %v", errUpdate)
	}
	_, errExec := execute("team")
	var refusal *accountPinError
	if !errors.As(errExec, &refusal) || refusal.StatusCode() != http.StatusTooManyRequests || !strings.Contains(refusal.Error(), "held until") {
		t.Fatalf("Execute(pin=team) on a held pin-only account = %v; want 429 held until", errExec)
	}
}
