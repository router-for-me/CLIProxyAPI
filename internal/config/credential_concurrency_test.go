package config

import (
	"testing"
	"time"
)

func TestCredentialConcurrencyLimiterConfig(t *testing.T) {
	got := (CredentialConcurrencyConfig{}).WithDefaults()
	if got.LifecycleConfigRevision != 0 || got.ObservationBarrierRevision != 0 {
		t.Fatalf("default revisions = %d, %d, want 0, 0", got.LifecycleConfigRevision, got.ObservationBarrierRevision)
	}
	if got.CPAHeartbeatTimeout != 3*time.Second || got.CPACancelBound != 5*time.Second || got.ReclaimGrace != 5*time.Second || got.CleanupInterval != 5*time.Second {
		t.Fatalf("default lifecycle config = %#v", got)
	}
	if got.ReleaseFlushInterval != 250*time.Millisecond || got.ReleaseMaxBackoff != 2*time.Second || got.BusyRetryMin != 250*time.Millisecond || got.BusyRetryMax != time.Second || got.MaxLimit != 1_000_000 {
		t.Fatalf("default limiter config = %#v", got)
	}
	if errValidate := ValidateCredentialConcurrencyLifecycle(20*time.Second, got); errValidate != nil {
		t.Fatalf("ValidateCredentialConcurrencyLifecycle() error = %v", errValidate)
	}
	if errValidate := ValidateCredentialConcurrencyLifecycle(2*time.Second, got); errValidate == nil {
		t.Fatal("ValidateCredentialConcurrencyLifecycle() error = nil, want timing invariant failure")
	}
}

func TestValidateCredentialConcurrencyAcceptsHomeAuthoritativeHeartbeat(t *testing.T) {
	cfg := (CredentialConcurrencyConfig{}).WithDefaults()
	cfg.CPAHeartbeatTimeout = 20 * time.Second

	if errValidate := ValidateCredentialConcurrency(cfg); errValidate != nil {
		t.Fatalf("ValidateCredentialConcurrency() error = %v", errValidate)
	}
	if errValidate := ValidateCredentialConcurrencyLifecycle(20*time.Second, cfg); errValidate == nil {
		t.Fatal("ValidateCredentialConcurrencyLifecycle() error = nil, want Home timing invariant failure")
	}
}

func TestCredentialConcurrencyConfigDefaultsOnlyMissingFields(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name: "explicit zero revision",
			payload: "credential-concurrency:\n" +
				"  lifecycle-config-revision: 0\n" +
				"  cpa-heartbeat-timeout: 3s\n" +
				"  cpa-cancel-bound: 5s\n" +
				"  reclaim-grace: 5s\n" +
				"  cleanup-interval: 5s\n",
		},
		{
			name: "explicit zero duration",
			payload: "credential-concurrency:\n" +
				"  lifecycle-config-revision: 1\n" +
				"  cpa-heartbeat-timeout: 0s\n" +
				"  cpa-cancel-bound: 5s\n" +
				"  reclaim-grace: 5s\n" +
				"  cleanup-interval: 5s\n",
		},
		{
			name: "explicit null duration",
			payload: "credential-concurrency:\n" +
				"  lifecycle-config-revision: 1\n" +
				"  cpa-heartbeat-timeout: null\n" +
				"  cpa-cancel-bound: 5s\n" +
				"  reclaim-grace: 5s\n" +
				"  cleanup-interval: 5s\n",
		},
		{
			name: "negative observation barrier",
			payload: "credential-concurrency:\n" +
				"  lifecycle-config-revision: 1\n" +
				"  observation-barrier-revision: -1\n" +
				"  cpa-heartbeat-timeout: 3s\n" +
				"  cpa-cancel-bound: 5s\n" +
				"  reclaim-grace: 5s\n" +
				"  cleanup-interval: 5s\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, errParse := ParseConfigBytes([]byte(test.payload))
			if errParse != nil {
				t.Fatalf("ParseConfigBytes() error = %v", errParse)
			}
			if errValidate := ValidateCredentialConcurrencyLifecycle(20*time.Second, parsed.CredentialConcurrency); errValidate == nil {
				t.Fatal("ValidateCredentialConcurrencyLifecycle() error = nil, want explicit invalid lifecycle value rejection")
			}
		})
	}
}

func TestCredentialConcurrencyConfigRejectsInvalidLimiter(t *testing.T) {
	tests := []CredentialConcurrencyConfig{
		{ReleaseFlushInterval: time.Second, ReleaseMaxBackoff: 500 * time.Millisecond, BusyRetryMin: time.Millisecond, BusyRetryMax: time.Millisecond, MaxLimit: 1},
		{ReleaseFlushInterval: time.Millisecond, ReleaseMaxBackoff: time.Millisecond, BusyRetryMin: 1500 * time.Microsecond, BusyRetryMax: 2 * time.Millisecond, MaxLimit: 1},
		{ReleaseFlushInterval: time.Millisecond, ReleaseMaxBackoff: time.Millisecond, BusyRetryMin: time.Millisecond, BusyRetryMax: time.Millisecond, MaxLimit: 1_000_001},
	}
	for _, cfg := range tests {
		cfg.CPAHeartbeatTimeout = 3 * time.Second
		cfg.CPACancelBound = 5 * time.Second
		cfg.ReclaimGrace = 5 * time.Second
		cfg.CleanupInterval = 5 * time.Second
		if errValidate := ValidateCredentialConcurrencyLifecycle(20*time.Second, cfg); errValidate == nil {
			t.Fatalf("ValidateCredentialConcurrencyLifecycle(%#v) error = nil", cfg)
		}
	}
}

func TestValidateCredentialConcurrencyLifecycleRejectsSafetyOverflow(t *testing.T) {
	cfg := CredentialConcurrencyConfig{
		LifecycleConfigRevision: 1,
		CPAHeartbeatTimeout:     time.Duration(1<<63 - 1),
		CPACancelBound:          time.Nanosecond,
		ReclaimGrace:            time.Second,
		CleanupInterval:         time.Second,
	}
	if errValidate := ValidateCredentialConcurrencyLifecycle(time.Second, cfg); errValidate == nil {
		t.Fatal("ValidateCredentialConcurrencyLifecycle() error = nil, want overflow rejection")
	}
}
