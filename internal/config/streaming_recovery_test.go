package config

import "testing"

func TestParseConfigBytesStreamingRecoveryDefaults(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("streaming:\n  recovery:\n    attempts: 2\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes error: %v", err)
	}
	r := cfg.Streaming.Recovery
	if r.Attempts != 2 || r.MaxBufferBytes != 8<<20 || r.MaxRetryWindowSeconds != 20 || r.MaxConcurrent != 16 || r.InitialBackoffMilliseconds != 250 || r.MaxBackoffMilliseconds != 2000 {
		t.Fatalf("unexpected normalized recovery: %+v", r)
	}
}

func TestParseConfigBytesStreamingRecoveryDurationOnly(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("streaming:\n  recovery:\n    enabled: true\n    attempts: 0\n    max-retry-window-seconds: 600\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes error: %v", err)
	}
	r := cfg.Streaming.Recovery
	if !r.Enabled || r.Attempts != 0 || r.MaxRetryWindowSeconds != 600 {
		t.Fatalf("unexpected duration-only recovery: %+v", r)
	}
	if r.MaxBufferBytes != 8<<20 || r.MaxConcurrent != 16 || r.InitialBackoffMilliseconds != 250 || r.MaxBackoffMilliseconds != 2000 {
		t.Fatalf("duration-only defaults not applied: %+v", r)
	}
}

func TestParseConfigBytesStreamingRecoveryDisabledPreservesZeroValues(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("streaming:\n  bootstrap-retries: -1\n  recovery:\n    attempts: 0\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes error: %v", err)
	}
	if cfg.Streaming.BootstrapRetries != 0 {
		t.Fatalf("bootstrap retries = %d, want 0", cfg.Streaming.BootstrapRetries)
	}
	if got := cfg.Streaming.Recovery; got != (StreamingRecoveryConfig{}) {
		t.Fatalf("disabled recovery changed subordinate values: %+v", got)
	}
}

func TestParseConfigBytesStreamingRecoveryNormalizesBounds(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("streaming:\n  recovery:\n    attempts: 9223372036854775807\n    max-buffer-bytes: 9223372036854775807\n    max-retry-window-seconds: 9223372036854775807\n    max-concurrent: 9223372036854775807\n    initial-backoff-milliseconds: 9223372036854775807\n    max-backoff-milliseconds: 10\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes error: %v", err)
	}
	r := cfg.Streaming.Recovery
	if r.Attempts != 10 || r.MaxBufferBytes != 64<<20 || r.MaxRetryWindowSeconds != 3600 || r.MaxConcurrent != 1024 || r.InitialBackoffMilliseconds != 30000 {
		t.Fatalf("hard bounds not applied: %+v", r)
	}
	if r.MaxBackoffMilliseconds != r.InitialBackoffMilliseconds {
		t.Fatalf("reversed backoff not clamped: %+v", r)
	}
}
