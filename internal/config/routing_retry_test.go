package config

import "testing"

func TestRoutingRetryConfig(t *testing.T) {
	const yamlConfig = `routing:
  retry:
    max-attempts: 5
    max-time-ms: 8000
    backoff-ms: 250
    retry-on: [408, 429, 503, 504]
`

	cfg, errParse := ParseConfigBytes([]byte(yamlConfig))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	got := cfg.Routing.Retry
	if got.MaxAttempts != 5 {
		t.Errorf("MaxAttempts = %d, want 5", got.MaxAttempts)
	}
	if got.MaxTimeMS != 8000 {
		t.Errorf("MaxTimeMS = %d, want 8000", got.MaxTimeMS)
	}
	if got.BackoffMS != 250 {
		t.Errorf("BackoffMS = %d, want 250", got.BackoffMS)
	}
	if len(got.RetryOn) != 4 || got.RetryOn[0] != 408 || got.RetryOn[3] != 504 {
		t.Errorf("RetryOn = %v, want [408 429 503 504]", got.RetryOn)
	}

	// Zero-value defaults round-trip: omitting the retry block yields a
	// zero-valued RetryConfig so callers can use the `RetryConfig{} == 0`
	// idiom (today: zero == disabled retry). RetryOn is a slice so we
	// compare its length only.
	const emptyConfig = `routing:
  strategy: "round-robin"
`
	emptyCfg, errParse := ParseConfigBytes([]byte(emptyConfig))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if emptyCfg.Routing.Retry.MaxAttempts != 0 ||
		emptyCfg.Routing.Retry.MaxTimeMS != 0 ||
		emptyCfg.Routing.Retry.BackoffMS != 0 ||
		len(emptyCfg.Routing.Retry.RetryOn) != 0 {
		t.Errorf("Retry = %+v, want zero value (MaxAttempts/MaxTimeMS/BackoffMS=0, RetryOn empty)", emptyCfg.Routing.Retry)
	}
}
