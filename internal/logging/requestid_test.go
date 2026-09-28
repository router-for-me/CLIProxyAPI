package logging

import (
	"sync"
	"testing"
)

func TestGenerateRequestID_Sequence(t *testing.T) {
	requestIDCounter.Store(0)

	expected := []string{
		"00000000",
		"00000001",
		"00000002",
		"00000003",
	}

	for _, want := range expected {
		if got := GenerateRequestID(); got != want {
			t.Fatalf("GenerateRequestID() = %q, want %q", got, want)
		}
	}
}

func TestGenerateRequestID_WrapAround(t *testing.T) {
	requestIDCounter.Store(0xFFFFFFFF)

	if got := GenerateRequestID(); got != "ffffffff" {
		t.Fatalf("GenerateRequestID() = %q, want ffffffff", got)
	}

	if got := GenerateRequestID(); got != "00000000" {
		t.Fatalf("GenerateRequestID() after wraparound = %q, want 00000000", got)
	}

	if got := GenerateRequestID(); got != "00000001" {
		t.Fatalf("GenerateRequestID() after wraparound = %q, want 00000001", got)
	}
}

func TestGenerateRequestID_Concurrency(t *testing.T) {
	requestIDCounter.Store(0)

	const total = 1000
	var wg sync.WaitGroup
	ids := make(chan string, total)

	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids <- GenerateRequestID()
		}()
	}

	wg.Wait()
	close(ids)

	seen := make(map[string]bool, total)
	for id := range ids {
		if len(id) != 8 {
			t.Fatalf("expected ID length 8, got %q (len %d)", id, len(id))
		}
		if seen[id] {
			t.Fatalf("duplicate ID generated in concurrent run: %q", id)
		}
		seen[id] = true
	}

	if len(seen) != total {
		t.Fatalf("expected %d unique IDs, got %d", total, len(seen))
	}
}
