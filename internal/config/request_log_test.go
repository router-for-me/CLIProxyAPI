package config

import (
	"testing"
	"time"
)

func TestParseConfig_RequestLogPolicyFields(t *testing.T) {
	t.Run("flat layout", func(t *testing.T) {
		raw := `
request-log: true
request-log-api-keys:
  - "key-one"
  - "key-two"
request-log-until: "2026-12-31T23:59:59Z"
`
		cfg, errParse := ParseConfigBytes([]byte(raw))
		if errParse != nil {
			t.Fatalf("ParseConfig error = %v", errParse)
		}
		if !cfg.RequestLog {
			t.Fatal("expected RequestLog to be true")
		}
		if len(cfg.RequestLogAPIKeys) != 2 || cfg.RequestLogAPIKeys[0] != "key-one" || cfg.RequestLogAPIKeys[1] != "key-two" {
			t.Fatalf("unexpected RequestLogAPIKeys: %#v", cfg.RequestLogAPIKeys)
		}
		if cfg.RequestLogUntil != "2026-12-31T23:59:59Z" {
			t.Fatalf("unexpected RequestLogUntil: %q", cfg.RequestLogUntil)
		}
	})

	t.Run("v8 layout", func(t *testing.T) {
		raw := `
config-version: 8
observability:
  logs:
    request-log: true
    request-log-api-keys:
      - "sk-admin"
    request-log-until: "2026-11-01T00:00:00Z"
`
		cfg, errParse := ParseConfigBytes([]byte(raw))
		if errParse != nil {
			t.Fatalf("ParseConfig error = %v", errParse)
		}
		if !cfg.RequestLog {
			t.Fatal("expected RequestLog to be true")
		}
		if len(cfg.RequestLogAPIKeys) != 1 || cfg.RequestLogAPIKeys[0] != "sk-admin" {
			t.Fatalf("unexpected RequestLogAPIKeys: %#v", cfg.RequestLogAPIKeys)
		}
		if cfg.RequestLogUntil != "2026-11-01T00:00:00Z" {
			t.Fatalf("unexpected RequestLogUntil: %q", cfg.RequestLogUntil)
		}

		if errValidate := ValidateV8Config([]byte(raw)); errValidate != nil {
			t.Fatalf("ValidateV8Config error = %v", errValidate)
		}
	})
}

func TestValidateRequestLog_Until(t *testing.T) {
	testCases := []struct {
		name    string
		until   string
		wantErr bool
	}{
		{name: "empty passes", until: "", wantErr: false},
		{name: "valid UTC RFC3339", until: "2026-10-21T15:04:05Z", wantErr: false},
		{name: "valid offset RFC3339", until: "2026-10-21T15:04:05+08:00", wantErr: false},
		{name: "valid nano RFC3339", until: "2026-10-21T15:04:05.123456Z", wantErr: false},
		{name: "invalid date format", until: "2026-10-21 15:04:05", wantErr: true},
		{name: "invalid text", until: "tomorrow", wantErr: true},
		{name: "whitespace only fails", until: "   ", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				SDKConfig: SDKConfig{
					RequestLog:      true,
					RequestLogUntil: tc.until,
				},
			}
			err := cfg.ValidateRequestLog()
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateRequestLog() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}

	t.Run("v8 validation rejects invalid until", func(t *testing.T) {
		raw := `
config-version: 8
observability:
  logs:
    request-log: true
    request-log-until: "not-rfc3339"
`
		if err := ValidateV8Config([]byte(raw)); err == nil {
			t.Fatal("expected ValidateV8Config to reject invalid request-log-until")
		}
	})
}

func TestRequestLogAdmitted_Policy(t *testing.T) {
	refTime := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	futureTime := refTime.Add(1 * time.Hour).Format(time.RFC3339)
	pastTime := refTime.Add(-1 * time.Hour).Format(time.RFC3339)
	exactTime := refTime.Format(time.RFC3339)

	t.Run("master switch off denies all", func(t *testing.T) {
		cfg := &Config{SDKConfig: SDKConfig{RequestLog: false}}
		if cfg.RequestLogAdmitted("any-key", refTime) {
			t.Fatal("expected denied when request-log is false")
		}
	})

	t.Run("commercial mode forces off", func(t *testing.T) {
		cfg := &Config{
			CommercialMode: true,
			SDKConfig:      SDKConfig{RequestLog: true},
		}
		if cfg.RequestLogAdmitted("any-key", refTime) {
			t.Fatal("expected denied when commercial-mode is true")
		}
	})

	t.Run("empty allowlist admitted globally", func(t *testing.T) {
		cfg := &Config{
			SDKConfig: SDKConfig{
				RequestLog:        true,
				RequestLogAPIKeys: nil,
			},
		}
		if !cfg.RequestLogAdmitted("any-key", refTime) {
			t.Fatal("expected admitted with key and empty allowlist")
		}
		if !cfg.RequestLogAdmitted("", refTime) {
			t.Fatal("expected admitted without principal and empty allowlist")
		}
	})

	t.Run("allowlist matching semantics", func(t *testing.T) {
		cfg := &Config{
			SDKConfig: SDKConfig{
				RequestLog:        true,
				RequestLogAPIKeys: []string{"sk-alice", "sk-bob"},
			},
		}
		if !cfg.RequestLogAdmitted("sk-alice", refTime) {
			t.Fatal("expected sk-alice to be admitted")
		}
		if !cfg.RequestLogAdmitted("sk-bob", refTime) {
			t.Fatal("expected sk-bob to be admitted")
		}
		if cfg.RequestLogAdmitted("sk-charlie", refTime) {
			t.Fatal("expected unlisted key to be denied")
		}
		if cfg.RequestLogAdmitted("", refTime) {
			t.Fatal("expected empty principal to be denied when allowlist is non-empty")
		}
	})

	t.Run("until deadline stops new captures at now >= until", func(t *testing.T) {
		cfgFuture := &Config{
			SDKConfig: SDKConfig{
				RequestLog:      true,
				RequestLogUntil: futureTime,
			},
		}
		if !cfgFuture.RequestLogAdmitted("any-key", refTime) {
			t.Fatal("expected admitted before until deadline")
		}

		cfgPast := &Config{
			SDKConfig: SDKConfig{
				RequestLog:      true,
				RequestLogUntil: pastTime,
			},
		}
		if cfgPast.RequestLogAdmitted("any-key", refTime) {
			t.Fatal("expected denied after until deadline")
		}

		cfgExact := &Config{
			SDKConfig: SDKConfig{
				RequestLog:      true,
				RequestLogUntil: exactTime,
			},
		}
		if cfgExact.RequestLogAdmitted("any-key", refTime) {
			t.Fatal("expected denied at exact until instant (now >= until)")
		}

		cfgInvalid := &Config{
			SDKConfig: SDKConfig{
				RequestLog:      true,
				RequestLogUntil: "invalid-timestamp",
			},
		}
		if cfgInvalid.RequestLogAdmitted("any-key", refTime) {
			t.Fatal("expected fail-closed when until is invalid")
		}
	})
}
