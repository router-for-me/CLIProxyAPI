package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type credentialConcurrencyFixtureWireConfig struct {
	LifecycleConfigRevision    int64         `json:"lifecycle-config-revision"`
	ObservationBarrierRevision int64         `json:"observation-barrier-revision"`
	CPAHeartbeatTimeout        time.Duration `json:"cpa-heartbeat-timeout"`
	CPACancelBound             time.Duration `json:"cpa-cancel-bound"`
	ReclaimGrace               time.Duration `json:"reclaim-grace"`
	CleanupInterval            time.Duration `json:"cleanup-interval"`
	ReleaseFlushInterval       string        `json:"release-flush-interval" yaml:"release-flush-interval"`
	ReleaseMaxBackoff          string        `json:"release-max-backoff" yaml:"release-max-backoff"`
	BusyRetryMin               string        `json:"busy-retry-min" yaml:"busy-retry-min"`
	BusyRetryMax               string        `json:"busy-retry-max" yaml:"busy-retry-max"`
	MaxLimit                   int64         `json:"max-limit"`
}

type credentialConcurrencyFixtureHotDurations struct {
	ReleaseFlushInterval time.Duration `yaml:"release-flush-interval"`
	ReleaseMaxBackoff    time.Duration `yaml:"release-max-backoff"`
	BusyRetryMin         time.Duration `yaml:"busy-retry-min"`
	BusyRetryMax         time.Duration `yaml:"busy-retry-max"`
}

func (c credentialConcurrencyFixtureWireConfig) config() (CredentialConcurrencyConfig, error) {
	raw, errMarshal := yaml.Marshal(c)
	if errMarshal != nil {
		return CredentialConcurrencyConfig{}, fmt.Errorf("marshal fixture hot durations as YAML: %w", errMarshal)
	}
	var hot credentialConcurrencyFixtureHotDurations
	if errUnmarshal := yaml.Unmarshal(raw, &hot); errUnmarshal != nil {
		return CredentialConcurrencyConfig{}, fmt.Errorf("parse fixture hot durations as YAML: %w", errUnmarshal)
	}
	return CredentialConcurrencyConfig{
		LifecycleConfigRevision:    c.LifecycleConfigRevision,
		ObservationBarrierRevision: c.ObservationBarrierRevision,
		CPAHeartbeatTimeout:        c.CPAHeartbeatTimeout,
		CPACancelBound:             c.CPACancelBound,
		ReclaimGrace:               c.ReclaimGrace,
		CleanupInterval:            c.CleanupInterval,
		ReleaseFlushInterval:       hot.ReleaseFlushInterval,
		ReleaseMaxBackoff:          hot.ReleaseMaxBackoff,
		BusyRetryMin:               hot.BusyRetryMin,
		BusyRetryMax:               hot.BusyRetryMax,
		MaxLimit:                   c.MaxLimit,
	}, nil
}

func TestCredentialConcurrencyLifecycleFixture(t *testing.T) {
	raw, errRead := os.ReadFile(filepath.Join("..", "..", "testdata", "credential-concurrency-lifecycle.json"))
	if errRead != nil {
		t.Fatal(errRead)
	}
	var fixture struct {
		Defaults credentialConcurrencyFixtureWireConfig `json:"defaults"`
		Invalid  []struct {
			NodeHeartbeatTimeout time.Duration                          `json:"node_heartbeat_timeout"`
			Config               credentialConcurrencyFixtureWireConfig `json:"config"`
		} `json:"invalid"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&fixture); errDecode != nil {
		t.Fatal(errDecode)
	}
	if errTrailing := decoder.Decode(&struct{}{}); errTrailing != io.EOF {
		t.Fatalf("fixture contains trailing JSON: %v", errTrailing)
	}

	defaults, errConfig := fixture.Defaults.config()
	if errConfig != nil {
		t.Fatal(errConfig)
	}

	expectedDefaults := CredentialConcurrencyConfig{
		LifecycleConfigRevision:    1,
		ObservationBarrierRevision: 0,
		CPAHeartbeatTimeout:        3 * time.Second,
		CPACancelBound:             5 * time.Second,
		ReclaimGrace:               5 * time.Second,
		CleanupInterval:            5 * time.Second,
		ReleaseFlushInterval:       250 * time.Millisecond,
		ReleaseMaxBackoff:          2 * time.Second,
		BusyRetryMin:               250 * time.Millisecond,
		BusyRetryMax:               time.Second,
		MaxLimit:                   1_000_000,
	}
	if defaults != expectedDefaults {
		t.Fatalf("defaults = %#v, want %#v", defaults, expectedDefaults)
	}
	if errValidate := ValidateCredentialConcurrency(defaults); errValidate != nil {
		t.Fatalf("ValidateCredentialConcurrency(defaults) error = %v", errValidate)
	}

	expectedInvalid := []struct {
		nodeHeartbeatTimeout time.Duration
		config               CredentialConcurrencyConfig
	}{
		{
			nodeHeartbeatTimeout: 3 * time.Second,
			config: CredentialConcurrencyConfig{
				CPAHeartbeatTimeout:  3 * time.Second,
				CPACancelBound:       5 * time.Second,
				ReclaimGrace:         5 * time.Second,
				CleanupInterval:      5 * time.Second,
				ReleaseFlushInterval: 250 * time.Millisecond,
				ReleaseMaxBackoff:    2 * time.Second,
				BusyRetryMin:         250 * time.Millisecond,
				BusyRetryMax:         time.Second,
				MaxLimit:             1_000_000,
			},
		},
		{
			nodeHeartbeatTimeout: 20 * time.Second,
			config: CredentialConcurrencyConfig{
				CPAHeartbeatTimeout:  0,
				CPACancelBound:       5 * time.Second,
				ReclaimGrace:         5 * time.Second,
				CleanupInterval:      5 * time.Second,
				ReleaseFlushInterval: 250 * time.Millisecond,
				ReleaseMaxBackoff:    2 * time.Second,
				BusyRetryMin:         250 * time.Millisecond,
				BusyRetryMax:         time.Second,
				MaxLimit:             1_000_000,
			},
		},
	}
	if len(fixture.Invalid) != len(expectedInvalid) {
		t.Fatalf("invalid fixture count = %d, want %d", len(fixture.Invalid), len(expectedInvalid))
	}
	for index, expected := range expectedInvalid {
		item := fixture.Invalid[index]
		itemConfig, errConfig := item.Config.config()
		if errConfig != nil {
			t.Fatalf("invalid fixture %d config() error = %v", index, errConfig)
		}
		if item.NodeHeartbeatTimeout != expected.nodeHeartbeatTimeout || itemConfig != expected.config {
			t.Fatalf("invalid fixture %d = %#v, want node heartbeat timeout %s and config %#v", index, itemConfig, expected.nodeHeartbeatTimeout, expected.config)
		}
		if errValidate := ValidateCredentialConcurrencyLifecycle(item.NodeHeartbeatTimeout, itemConfig); errValidate == nil {
			t.Fatalf("invalid fixture %d passed", index)
		}
	}
}
