package auth

import (
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestQuotaStateQuotaResetTimeParsesSupportedHeaders(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	future := now.Add(4 * time.Hour)

	tests := []struct {
		name     string
		provider string
		signals  map[string]string
		want     time.Time
		wantOK   bool
	}{
		{
			name:     "anthropic unified reset absolute seconds",
			provider: "claude",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-Reset": strconv.FormatInt(future.Unix(), 10),
			},
			want:   future,
			wantOK: true,
		},
		{
			name:     "anthropic legacy reset absolute seconds",
			provider: "claude",
			signals: map[string]string{
				"anthropic-ratelimit-reset": strconv.FormatInt(future.Unix(), 10),
			},
			want:   future,
			wantOK: true,
		},
		{
			name:     "codex reset at absolute seconds",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Primary-Reset-At": strconv.FormatInt(future.Unix(), 10),
			},
			want:   future,
			wantOK: true,
		},
		{
			name:     "codex reset after seconds is relative",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Primary-Reset-After-Seconds": "309718",
			},
			want:   now.Add(309718 * time.Second),
			wantOK: true,
		},
		{
			name:     "unified reset wins over legacy reset",
			provider: "claude",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-Reset": strconv.FormatInt(future.Unix(), 10),
				"anthropic-ratelimit-reset":         strconv.FormatInt(future.Add(time.Hour).Unix(), 10),
			},
			want:   future,
			wantOK: true,
		},
		{
			name:     "stale absolute reset is ignored",
			provider: "claude",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-Reset": strconv.FormatInt(now.Add(-time.Minute).Unix(), 10),
			},
			wantOK: false,
		},
		{
			name:     "absolute reset equal to now is ignored",
			provider: "claude",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-Reset": strconv.FormatInt(now.Unix(), 10),
			},
			wantOK: false,
		},
		{
			name:     "malformed values are ignored",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Primary-Reset-At":            "not-a-number",
				"X-Codex-Primary-Reset-After-Seconds": "1e300",
			},
			wantOK: false,
		},
		{
			name:     "negative relative reset is ignored",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Primary-Reset-After-Seconds": "-30",
			},
			wantOK: false,
		},
		{
			name:     "provider scoped headers are not borrowed",
			provider: "claude",
			signals: map[string]string{
				"X-Codex-Primary-Reset-At": strconv.FormatInt(future.Unix(), 10),
			},
			wantOK: false,
		},
		{
			name:     "unknown provider reads every supported header",
			provider: "custom",
			signals: map[string]string{
				"X-Codex-Primary-Reset-At": strconv.FormatInt(future.Unix(), 10),
			},
			want:   future,
			wantOK: true,
		},
		{
			name:     "case insensitive signal keys",
			provider: "codex",
			signals: map[string]string{
				"x-codex-primary-reset-at": strconv.FormatInt(future.Unix(), 10),
			},
			want:   future,
			wantOK: true,
		},
		{
			name:     "empty signals",
			provider: "claude",
			signals:  nil,
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			quota := QuotaState{Signals: tt.signals}
			before := quota.Clone()
			got, ok := quota.QuotaResetTime(tt.provider, now)
			if ok != tt.wantOK {
				t.Fatalf("QuotaResetTime() ok = %v, want %v (got %v)", ok, tt.wantOK, got)
			}
			if ok && !got.Equal(tt.want) {
				t.Fatalf("QuotaResetTime() = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(quota.Signals, before.Signals) {
				t.Fatalf("QuotaResetTime() mutated signals: %#v -> %#v", before.Signals, quota.Signals)
			}
		})
	}
}

func TestAuthQuotaWindowResetTimeDerivesFromAnchor(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	window := 5 * time.Hour

	auth := &Auth{
		Provider: "claude",
		Attributes: map[string]string{
			AttributeQuotaWindowSeconds: strconv.Itoa(int(window / time.Second)),
		},
		Metadata: map[string]any{
			MetadataQuotaWindowAnchor: now.Add(-4 * time.Hour).Format(time.RFC3339),
		},
	}
	got, ok := auth.resetTimeFromQuota(now)
	if !ok {
		t.Fatal("resetTimeFromQuota() did not derive a window reset")
	}
	if want := now.Add(time.Hour); !got.Equal(want) {
		t.Fatalf("resetTimeFromQuota() = %v, want %v", got, want)
	}

	auth.Metadata[MetadataQuotaWindowAnchor] = now.Add(-window).Format(time.RFC3339)
	if _, okExpired := auth.resetTimeFromQuota(now); okExpired {
		t.Fatal("expired window reset was treated as usable")
	}

	if _, okMissingAnchor := (&Auth{
		Provider: "claude",
		Attributes: map[string]string{
			AttributeQuotaWindowSeconds: strconv.Itoa(int(window / time.Second)),
		},
	}).resetTimeFromQuota(now); okMissingAnchor {
		t.Fatal("window reset was derived without an anchor")
	}

	if _, okNoWindow := (&Auth{
		Metadata: map[string]any{
			MetadataQuotaWindowAnchor: now.Add(-time.Hour).Format(time.RFC3339),
		},
	}).resetTimeFromQuota(now); okNoWindow {
		t.Fatal("window reset was derived without a configured window")
	}
}

func TestAuthSignalsTakePrecedenceOverWindowAnchor(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	auth := &Auth{
		Provider: "claude",
		Attributes: map[string]string{
			AttributeQuotaWindowSeconds: strconv.Itoa(int(5 * time.Hour / time.Second)),
		},
		Metadata: map[string]any{
			// Anchor alone would imply a reset 5 minutes from now.
			MetadataQuotaWindowAnchor: now.Add(-5*time.Hour + 5*time.Minute).Format(time.RFC3339),
		},
		Quota: QuotaState{Signals: map[string]string{
			"Anthropic-Ratelimit-Unified-Reset": strconv.FormatInt(now.Add(3*time.Hour).Unix(), 10),
		}},
	}
	got, ok := auth.resetTimeFromQuota(now)
	if !ok {
		t.Fatal("resetTimeFromQuota() = false, want signal reset")
	}
	if want := now.Add(3 * time.Hour); !got.Equal(want) {
		t.Fatalf("resetTimeFromQuota() = %v, want signal value %v", got, want)
	}
}

func TestEnsureQuotaWindowAnchorWritesOnceForWindowedAuths(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	auth := &Auth{
		Attributes: map[string]string{AttributeQuotaWindowSeconds: "18000"},
	}
	auth.ensureQuotaWindowAnchor(now)
	raw, ok := auth.Metadata[MetadataQuotaWindowAnchor].(string)
	if !ok || raw == "" {
		t.Fatalf("anchor was not written: %#v", auth.Metadata)
	}
	if want := now.Format(time.RFC3339); raw != want {
		t.Fatalf("anchor = %q, want %q", raw, want)
	}

	later := now.Add(time.Hour)
	auth.ensureQuotaWindowAnchor(later)
	if got := auth.Metadata[MetadataQuotaWindowAnchor]; got != raw {
		t.Fatalf("anchor was overwritten: %v -> %v", raw, got)
	}

	withoutWindow := &Auth{}
	withoutWindow.ensureQuotaWindowAnchor(now)
	if len(withoutWindow.Metadata) != 0 {
		t.Fatalf("anchor written without a configured window: %#v", withoutWindow.Metadata)
	}
}

func TestQuotaResetTimeRelativeOverflowIsRejected(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	quota := QuotaState{Signals: map[string]string{
		"X-Codex-Primary-Reset-After-Seconds": fmt.Sprintf("%d", maxQuotaWindowSeconds+1),
	}}
	if reset, ok := quota.QuotaResetTime("codex", now); ok {
		t.Fatalf("overflowing relative reset = %v, want rejected", reset)
	}
}
