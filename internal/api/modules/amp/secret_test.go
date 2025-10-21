package amp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestMultiSourceSecret_PrecedenceOrder(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name      string
		configKey string
		envKey    string
		fileJSON  string
		want      string
	}{
		{"config_wins", "cfg", "env", `{"apiKey@https://ampcode.com/":"file"}`, "cfg"},
		{"env_wins_when_no_cfg", "", "env", `{"apiKey@https://ampcode.com/":"file"}`, "env"},
		{"file_when_no_cfg_env", "", "", `{"apiKey@https://ampcode.com/":"file"}`, "file"},
		{"empty_cfg_trims_then_env", "   ", "env", `{"apiKey@https://ampcode.com/":"file"}`, "env"},
		{"empty_env_then_file", "", "   ", `{"apiKey@https://ampcode.com/":"file"}`, "file"},
		{"missing_file_returns_empty", "", "", "", ""},
		{"all_empty_returns_empty", "  ", "  ", `{"apiKey@https://ampcode.com/":"  "}`, ""},
	}

	for _, tc := range cases {
		tc := tc // capture range variable
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			secretsPath := filepath.Join(tmpDir, "secrets.json")

			if tc.fileJSON != "" {
				if err := os.WriteFile(secretsPath, []byte(tc.fileJSON), 0600); err != nil {
					t.Fatal(err)
				}
			}

			t.Setenv("AMP_API_KEY", tc.envKey)

			s := NewMultiSourceSecretWithPath(tc.configKey, secretsPath, 100*time.Millisecond)
			got, err := s.Get(ctx)
			if err != nil && tc.fileJSON != "" && json.Valid([]byte(tc.fileJSON)) {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestMultiSourceSecret_CacheBehavior(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "secrets.json")

	// Initial value
	if err := os.WriteFile(p, []byte(`{"apiKey@https://ampcode.com/":"v1"}`), 0600); err != nil {
		t.Fatal(err)
	}

	s := NewMultiSourceSecretWithPath("", p, 50*time.Millisecond)

	// First read - should return v1
	got1, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got1 != "v1" {
		t.Fatalf("expected v1, got %s", got1)
	}

	// Change file; within TTL we should still see v1 (cached)
	if err := os.WriteFile(p, []byte(`{"apiKey@https://ampcode.com/":"v2"}`), 0600); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.Get(ctx)
	if got2 != "v1" {
		t.Fatalf("cache hit expected v1, got %s", got2)
	}

	// After TTL expires, should see v2
	time.Sleep(60 * time.Millisecond)
	got3, _ := s.Get(ctx)
	if got3 != "v2" {
		t.Fatalf("cache miss expected v2, got %s", got3)
	}

	// Invalidate forces re-read immediately
	if err := os.WriteFile(p, []byte(`{"apiKey@https://ampcode.com/":"v3"}`), 0600); err != nil {
		t.Fatal(err)
	}
	s.InvalidateCache()
	got4, _ := s.Get(ctx)
	if got4 != "v3" {
		t.Fatalf("invalidate expected v3, got %s", got4)
	}
}

func TestMultiSourceSecret_FileHandling(t *testing.T) {
	ctx := context.Background()

	t.Run("missing_file_no_error", func(t *testing.T) {
		s := NewMultiSourceSecretWithPath("", "/nonexistent/path/secrets.json", 100*time.Millisecond)
		got, err := s.Get(ctx)
		if err != nil {
			t.Fatalf("expected no error for missing file, got: %v", err)
		}
		if got != "" {
			t.Fatalf("expected empty string, got %q", got)
		}
	})

	t.Run("invalid_json", func(t *testing.T) {
		tmpDir := t.TempDir()
		p := filepath.Join(tmpDir, "secrets.json")
		if err := os.WriteFile(p, []byte(`{invalid json`), 0600); err != nil {
			t.Fatal(err)
		}

		s := NewMultiSourceSecretWithPath("", p, 100*time.Millisecond)
		_, err := s.Get(ctx)
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})

	t.Run("missing_key_in_json", func(t *testing.T) {
		tmpDir := t.TempDir()
		p := filepath.Join(tmpDir, "secrets.json")
		if err := os.WriteFile(p, []byte(`{"other":"value"}`), 0600); err != nil {
			t.Fatal(err)
		}

		s := NewMultiSourceSecretWithPath("", p, 100*time.Millisecond)
		got, err := s.Get(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Fatalf("expected empty string for missing key, got %q", got)
		}
	})

	t.Run("empty_key_value", func(t *testing.T) {
		tmpDir := t.TempDir()
		p := filepath.Join(tmpDir, "secrets.json")
		if err := os.WriteFile(p, []byte(`{"apiKey@https://ampcode.com/":"   "}`), 0600); err != nil {
			t.Fatal(err)
		}

		s := NewMultiSourceSecretWithPath("", p, 100*time.Millisecond)
		got, _ := s.Get(ctx)
		if got != "" {
			t.Fatalf("expected empty after trim, got %q", got)
		}
	})
}

func TestMultiSourceSecret_Concurrency(t *testing.T) {
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "secrets.json")
	if err := os.WriteFile(p, []byte(`{"apiKey@https://ampcode.com/":"concurrent"}`), 0600); err != nil {
		t.Fatal(err)
	}

	s := NewMultiSourceSecretWithPath("", p, 5*time.Second)
	ctx := context.Background()

	// Spawn many goroutines calling Get concurrently
	const goroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	errors := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				val, err := s.Get(ctx)
				if err != nil {
					errors <- err
					return
				}
				if val != "concurrent" {
					errors <- err
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("concurrency error: %v", err)
	}
}

func TestStaticSecretSource(t *testing.T) {
	ctx := context.Background()

	t.Run("returns_provided_key", func(t *testing.T) {
		s := NewStaticSecretSource("test-key-123")
		got, err := s.Get(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "test-key-123" {
			t.Fatalf("want test-key-123, got %q", got)
		}
	})

	t.Run("trims_whitespace", func(t *testing.T) {
		s := NewStaticSecretSource("  test-key  ")
		got, err := s.Get(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "test-key" {
			t.Fatalf("want test-key, got %q", got)
		}
	})

	t.Run("empty_string", func(t *testing.T) {
		s := NewStaticSecretSource("")
		got, err := s.Get(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Fatalf("want empty string, got %q", got)
		}
	})
}

func TestMultiSourceSecret_CacheEmptyResult(t *testing.T) {
	// Test that missing file results are cached to avoid repeated file reads
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "nonexistent.json")

	s := NewMultiSourceSecretWithPath("", p, 100*time.Millisecond)
	ctx := context.Background()

	// First call - file doesn't exist, should cache empty result
	got1, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if got1 != "" {
		t.Fatalf("expected empty string, got %q", got1)
	}

	// Create the file now
	if err := os.WriteFile(p, []byte(`{"apiKey@https://ampcode.com/":"new-value"}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Second call - should still return empty (cached), not read the new file
	got2, _ := s.Get(ctx)
	if got2 != "" {
		t.Fatalf("cache should return empty, got %q", got2)
	}

	// After TTL expires, should see the new value
	time.Sleep(110 * time.Millisecond)
	got3, _ := s.Get(ctx)
	if got3 != "new-value" {
		t.Fatalf("after cache expiry, expected new-value, got %q", got3)
	}
}
