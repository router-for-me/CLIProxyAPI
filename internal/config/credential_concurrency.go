package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultCPAHeartbeatTimeout          = 3 * time.Second
	defaultCPACancelBound               = 5 * time.Second
	defaultReclaimGrace                 = 5 * time.Second
	defaultCleanupInterval              = 5 * time.Second
	defaultReleaseFlushInterval         = 250 * time.Millisecond
	defaultReleaseMaxBackoff            = 2 * time.Second
	defaultBusyRetryMin                 = 250 * time.Millisecond
	defaultBusyRetryMax                 = time.Second
	maxCredentialConcurrencyLimit int64 = 1_000_000
)

// CredentialConcurrencyConfig controls the credential concurrency lifecycle managed by Home.
type CredentialConcurrencyConfig struct {
	LifecycleConfigRevision    int64         `yaml:"lifecycle-config-revision" json:"lifecycle-config-revision"`
	ObservationBarrierRevision int64         `yaml:"observation-barrier-revision" json:"observation-barrier-revision"`
	CPAHeartbeatTimeout        time.Duration `yaml:"cpa-heartbeat-timeout" json:"cpa-heartbeat-timeout"`
	CPACancelBound             time.Duration `yaml:"cpa-cancel-bound" json:"cpa-cancel-bound"`
	ReclaimGrace               time.Duration `yaml:"reclaim-grace" json:"reclaim-grace"`
	CleanupInterval            time.Duration `yaml:"cleanup-interval" json:"cleanup-interval"`
	ReleaseFlushInterval       time.Duration `yaml:"release-flush-interval" json:"release-flush-interval"`
	ReleaseMaxBackoff          time.Duration `yaml:"release-max-backoff" json:"release-max-backoff"`
	BusyRetryMin               time.Duration `yaml:"busy-retry-min" json:"busy-retry-min"`
	BusyRetryMax               time.Duration `yaml:"busy-retry-max" json:"busy-retry-max"`
	MaxLimit                   int64         `yaml:"max-limit" json:"max-limit"`

	lifecycleConfigRevisionPresent    bool
	observationBarrierRevisionPresent bool
	cpaHeartbeatTimeoutPresent        bool
	cpaCancelBoundPresent             bool
	reclaimGracePresent               bool
	cleanupIntervalPresent            bool
	releaseFlushIntervalPresent       bool
	releaseMaxBackoffPresent          bool
	busyRetryMinPresent               bool
	busyRetryMaxPresent               bool
	maxLimitPresent                   bool
}

// UnmarshalYAML preserves field presence so only absent lifecycle values receive legacy defaults.
func (c *CredentialConcurrencyConfig) UnmarshalYAML(value *yaml.Node) error {
	type rawCredentialConcurrencyConfig struct {
		LifecycleConfigRevision    int64         `yaml:"lifecycle-config-revision"`
		ObservationBarrierRevision int64         `yaml:"observation-barrier-revision"`
		CPAHeartbeatTimeout        time.Duration `yaml:"cpa-heartbeat-timeout"`
		CPACancelBound             time.Duration `yaml:"cpa-cancel-bound"`
		ReclaimGrace               time.Duration `yaml:"reclaim-grace"`
		CleanupInterval            time.Duration `yaml:"cleanup-interval"`
		ReleaseFlushInterval       time.Duration `yaml:"release-flush-interval"`
		ReleaseMaxBackoff          time.Duration `yaml:"release-max-backoff"`
		BusyRetryMin               time.Duration `yaml:"busy-retry-min"`
		BusyRetryMax               time.Duration `yaml:"busy-retry-max"`
		MaxLimit                   int64         `yaml:"max-limit"`
	}

	var raw rawCredentialConcurrencyConfig
	if errDecode := value.Decode(&raw); errDecode != nil {
		return errDecode
	}

	*c = CredentialConcurrencyConfig{
		LifecycleConfigRevision:           raw.LifecycleConfigRevision,
		ObservationBarrierRevision:        raw.ObservationBarrierRevision,
		CPAHeartbeatTimeout:               raw.CPAHeartbeatTimeout,
		CPACancelBound:                    raw.CPACancelBound,
		ReclaimGrace:                      raw.ReclaimGrace,
		CleanupInterval:                   raw.CleanupInterval,
		ReleaseFlushInterval:              raw.ReleaseFlushInterval,
		ReleaseMaxBackoff:                 raw.ReleaseMaxBackoff,
		BusyRetryMin:                      raw.BusyRetryMin,
		BusyRetryMax:                      raw.BusyRetryMax,
		MaxLimit:                          raw.MaxLimit,
		lifecycleConfigRevisionPresent:    credentialConcurrencyFieldPresent(value, "lifecycle-config-revision"),
		observationBarrierRevisionPresent: credentialConcurrencyFieldPresent(value, "observation-barrier-revision"),
		cpaHeartbeatTimeoutPresent:        credentialConcurrencyFieldPresent(value, "cpa-heartbeat-timeout"),
		cpaCancelBoundPresent:             credentialConcurrencyFieldPresent(value, "cpa-cancel-bound"),
		reclaimGracePresent:               credentialConcurrencyFieldPresent(value, "reclaim-grace"),
		cleanupIntervalPresent:            credentialConcurrencyFieldPresent(value, "cleanup-interval"),
		releaseFlushIntervalPresent:       credentialConcurrencyFieldPresent(value, "release-flush-interval"),
		releaseMaxBackoffPresent:          credentialConcurrencyFieldPresent(value, "release-max-backoff"),
		busyRetryMinPresent:               credentialConcurrencyFieldPresent(value, "busy-retry-min"),
		busyRetryMaxPresent:               credentialConcurrencyFieldPresent(value, "busy-retry-max"),
		maxLimitPresent:                   credentialConcurrencyFieldPresent(value, "max-limit"),
	}
	return nil
}

func credentialConcurrencyFieldPresent(value *yaml.Node, field string) bool {
	if value == nil || value.Kind != yaml.MappingNode {
		return false
	}
	for index := 0; index+1 < len(value.Content); index += 2 {
		if value.Content[index].Value == field {
			return true
		}
	}
	return false
}

// WithDefaults applies the lifecycle defaults required for compatibility with older Home versions.
func (c CredentialConcurrencyConfig) WithDefaults() CredentialConcurrencyConfig {
	if !c.cpaHeartbeatTimeoutPresent && c.CPAHeartbeatTimeout == 0 {
		c.CPAHeartbeatTimeout = defaultCPAHeartbeatTimeout
	}
	if !c.cpaCancelBoundPresent && c.CPACancelBound == 0 {
		c.CPACancelBound = defaultCPACancelBound
	}
	if !c.reclaimGracePresent && c.ReclaimGrace == 0 {
		c.ReclaimGrace = defaultReclaimGrace
	}
	if !c.cleanupIntervalPresent && c.CleanupInterval == 0 {
		c.CleanupInterval = defaultCleanupInterval
	}
	if !c.releaseFlushIntervalPresent && c.ReleaseFlushInterval == 0 {
		c.ReleaseFlushInterval = defaultReleaseFlushInterval
	}
	if !c.releaseMaxBackoffPresent && c.ReleaseMaxBackoff == 0 {
		c.ReleaseMaxBackoff = defaultReleaseMaxBackoff
	}
	if !c.busyRetryMinPresent && c.BusyRetryMin == 0 {
		c.BusyRetryMin = defaultBusyRetryMin
	}
	if !c.busyRetryMaxPresent && c.BusyRetryMax == 0 {
		c.BusyRetryMax = defaultBusyRetryMax
	}
	if !c.maxLimitPresent && c.MaxLimit == 0 {
		c.MaxLimit = maxCredentialConcurrencyLimit
	}
	return c
}

// ValidateCredentialConcurrency validates values intrinsic to a credential concurrency configuration.
func ValidateCredentialConcurrency(cfg CredentialConcurrencyConfig) error {
	if cfg.LifecycleConfigRevision < 0 || (cfg.lifecycleConfigRevisionPresent && cfg.LifecycleConfigRevision == 0) {
		return fmt.Errorf("lifecycle configuration revision must be positive when present")
	}
	if cfg.ObservationBarrierRevision < 0 {
		return fmt.Errorf("observation barrier revision must not be negative")
	}
	if cfg.CPAHeartbeatTimeout <= 0 || cfg.CPACancelBound <= 0 || cfg.ReclaimGrace <= 0 || cfg.CleanupInterval <= 0 {
		return fmt.Errorf("credential concurrency lifecycle durations must be positive")
	}
	if cfg.ReleaseFlushInterval <= 0 || cfg.ReleaseMaxBackoff <= 0 || cfg.BusyRetryMin <= 0 || cfg.BusyRetryMax <= 0 {
		return fmt.Errorf("credential concurrency limiter durations must be positive")
	}
	if cfg.ReleaseMaxBackoff < cfg.ReleaseFlushInterval {
		return fmt.Errorf("credential concurrency release max backoff must not be less than release flush interval")
	}
	if cfg.BusyRetryMin%time.Millisecond != 0 || cfg.BusyRetryMax%time.Millisecond != 0 {
		return fmt.Errorf("credential concurrency busy retry durations must be whole milliseconds")
	}
	if cfg.BusyRetryMax < cfg.BusyRetryMin {
		return fmt.Errorf("credential concurrency busy retry max must not be less than busy retry min")
	}
	if cfg.MaxLimit < 1 || cfg.MaxLimit > maxCredentialConcurrencyLimit {
		return fmt.Errorf("credential concurrency max limit must be between 1 and %d", maxCredentialConcurrencyLimit)
	}
	return nil
}

// ValidateCredentialConcurrencyLifecycle verifies the Home lifecycle timing safety invariant.
func ValidateCredentialConcurrencyLifecycle(nodeHeartbeatTimeout time.Duration, cfg CredentialConcurrencyConfig) error {
	if nodeHeartbeatTimeout <= 0 {
		return fmt.Errorf("credential concurrency lifecycle durations must be positive")
	}
	if errValidate := ValidateCredentialConcurrency(cfg); errValidate != nil {
		return errValidate
	}
	left, leftOverflow := addCredentialConcurrencyDuration(nodeHeartbeatTimeout, cfg.ReclaimGrace)
	right, rightOverflow := addCredentialConcurrencyDuration(cfg.CPAHeartbeatTimeout, cfg.CPACancelBound)
	if leftOverflow || rightOverflow {
		return fmt.Errorf("credential concurrency lifecycle timing safety invariant overflows")
	}
	if left <= right {
		return fmt.Errorf("node heartbeat timeout plus reclaim grace must exceed CPA heartbeat timeout plus cancel bound")
	}
	return nil
}

func addCredentialConcurrencyDuration(left time.Duration, right time.Duration) (time.Duration, bool) {
	if right > 0 && left > time.Duration(1<<63-1)-right {
		return 0, true
	}
	return left + right, false
}
