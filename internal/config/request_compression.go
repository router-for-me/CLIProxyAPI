package config

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// RequestCompressionOff disables upstream request body compression.
	RequestCompressionOff = "off"
	// RequestCompressionAuto applies the provider's verified compression encoding.
	RequestCompressionAuto = "auto"

	// DefaultRequestCompressionMinBytes is the default minimum uncompressed body
	// size required before upstream request compression is applied.
	DefaultRequestCompressionMinBytes = 16 << 10 // 16 KiB
)

// NormalizeRequestCompression normalizes and validates request compression
// configuration values. Empty values retain their serialized form and use
// effective defaults at runtime.
func (cfg *Config) NormalizeRequestCompression() error {
	if cfg == nil {
		return nil
	}

	mode := strings.ToLower(strings.TrimSpace(cfg.RequestCompression))
	switch mode {
	case "", RequestCompressionOff, RequestCompressionAuto:
		cfg.RequestCompression = mode
	default:
		return fmt.Errorf("invalid request-compression value %q (allowed: auto, off)", cfg.RequestCompression)
	}

	_, normalizedMinSize, err := parseRequestCompressionMinSize(cfg.RequestCompressionMinSize)
	if err != nil {
		return err
	}
	cfg.RequestCompressionMinSize = normalizedMinSize
	return nil
}

// EffectiveRequestCompressionMode returns the mode used at runtime. Invalid
// programmatic values fail closed and disable compression.
func (cfg *Config) EffectiveRequestCompressionMode() string {
	if cfg == nil {
		return RequestCompressionOff
	}

	switch strings.ToLower(strings.TrimSpace(cfg.RequestCompression)) {
	case RequestCompressionAuto:
		return RequestCompressionAuto
	case "", RequestCompressionOff:
		return RequestCompressionOff
	default:
		return RequestCompressionOff
	}
}

// EffectiveRequestCompressionMinBytes returns the configured request size
// threshold in bytes. Invalid programmatic values use the default threshold.
func (cfg *Config) EffectiveRequestCompressionMinBytes() int {
	if cfg == nil {
		return DefaultRequestCompressionMinBytes
	}

	minBytes, _, err := parseRequestCompressionMinSize(cfg.RequestCompressionMinSize)
	if err != nil {
		return DefaultRequestCompressionMinBytes
	}
	return minBytes
}

func parseRequestCompressionMinSize(raw string) (int, string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return DefaultRequestCompressionMinBytes, "", nil
	}
	if len(value) < 2 || value[len(value)-1] != 'k' && value[len(value)-1] != 'K' {
		return 0, "", fmt.Errorf("invalid request-compression-min-size value %q (expected a positive integer followed by k, for example 16k)", raw)
	}

	kibibytes, err := strconv.ParseUint(value[:len(value)-1], 10, 64)
	if err != nil || kibibytes == 0 {
		return 0, "", fmt.Errorf("invalid request-compression-min-size value %q (expected a positive integer followed by k, for example 16k)", raw)
	}

	const bytesPerKiB = 1024
	maxInt := uint64(^uint(0) >> 1)
	if kibibytes > maxInt/bytesPerKiB {
		return 0, "", fmt.Errorf("request-compression-min-size value %q is too large", raw)
	}

	return int(kibibytes * bytesPerKiB), strconv.FormatUint(kibibytes, 10) + "k", nil
}
