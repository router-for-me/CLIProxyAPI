package common

import (
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestNewUTF8StreamParser(t *testing.T) {
	p := NewUTF8StreamParser()
	if p == nil {
		t.Fatal("expected non-nil UTF8StreamParser")
	}
	if p.buffer == nil {
		t.Error("expected non-nil buffer")
	}
}

func TestWrite(t *testing.T) {
	p := NewUTF8StreamParser()
	p.Write([]byte("hello"))

	result, ok := p.Read()
	if !ok {
		t.Error("expected ok to be true")
	}
	if result != "hello" {
		t.Errorf("expected 'hello', got '%s'", result)
	}
}

func TestWrite_MultipleWrites(t *testing.T) {
	p := NewUTF8StreamParser()
	p.Write([]byte("hel"))
	p.Write([]byte("lo"))

	result, ok := p.Read()
	if !ok {
		t.Error("expected ok to be true")
	}
	if result != "hello" {
		t.Errorf("expected 'hello', got '%s'", result)
	}
}

func TestRead_EmptyBuffer(t *testing.T) {
	p := NewUTF8StreamParser()
	result, ok := p.Read()
	if ok {
		t.Error("expected ok to be false for empty buffer")
	}
	if result != "" {
		t.Errorf("expected empty string, got '%s'", result)
	}
}

func TestRead_IncompleteUTF8(t *testing.T) {
	p := NewUTF8StreamParser()

	// Write incomplete multi-byte UTF-8 character
	// 中 (U+4E2D) = E4 B8 AD
	p.Write([]byte{0xE4, 0xB8})

	result, ok := p.Read()
	if ok {
		t.Error("expected ok to be false for incomplete UTF-8")
	}
	if result != "" {
		t.Errorf("expected empty string, got '%s'", result)
	}

	// Complete the character
	p.Write([]byte{0xAD})
	result, ok = p.Read()
	if !ok {
		t.Error("expected ok to be true after completing UTF-8")
	}
	if result != "中" {
		t.Errorf("expected '中', got '%s'", result)
	}
}

func TestRead_MixedASCIIAndUTF8(t *testing.T) {
	p := NewUTF8StreamParser()
	p.Write([]byte("Hello 世界"))

	result, ok := p.Read()
	if !ok {
		t.Error("expected ok to be true")
	}
	if result != "Hello 世界" {
		t.Errorf("expected 'Hello 世界', got '%s'", result)
	}
}

func TestRead_PartialMultibyteAtEnd(t *testing.T) {
	p := NewUTF8StreamParser()
	// "Hello" + partial "世" (E4 B8 96)
	p.Write([]byte("Hello"))
	p.Write([]byte{0xE4, 0xB8})

	result, ok := p.Read()
	if !ok {
		t.Error("expected ok to be true for valid portion")
	}
	if result != "Hello" {
		t.Errorf("expected 'Hello', got '%s'", result)
	}

	// Complete the character
	p.Write([]byte{0x96})
	result, ok = p.Read()
	if !ok {
		t.Error("expected ok to be true after completing")
	}
	if result != "世" {
		t.Errorf("expected '世', got '%s'", result)
	}
}

func TestFlush(t *testing.T) {
	p := NewUTF8StreamParser()
	p.Write([]byte("hello"))

	result := p.Flush()
	if result != "hello" {
		t.Errorf("expected 'hello', got '%s'", result)
	}

	// Verify buffer is cleared
	result2, ok := p.Read()
	if ok {
		t.Error("expected ok to be false after flush")
	}
	if result2 != "" {
		t.Errorf("expected empty string after flush, got '%s'", result2)
	}
}

func TestFlush_EmptyBuffer(t *testing.T) {
	p := NewUTF8StreamParser()
	result := p.Flush()
	if result != "" {
		t.Errorf("expected empty string, got '%s'", result)
	}
}

func TestFlush_IncompleteUTF8(t *testing.T) {
	p := NewUTF8StreamParser()
	p.Write([]byte{0xE4, 0xB8})

	result := p.Flush()
	// Flush returns everything including incomplete bytes
	if len(result) != 2 {
		t.Errorf("expected 2 bytes flushed, got %d", len(result))
	}
}

func TestReset(t *testing.T) {
	p := NewUTF8StreamParser()
	p.Write([]byte("hello"))
	p.Reset()

	result, ok := p.Read()
	if ok {
		t.Error("expected ok to be false after reset")
	}
	if result != "" {
		t.Errorf("expected empty string after reset, got '%s'", result)
	}
}

func TestUtf8CharSize(t *testing.T) {
	p := NewUTF8StreamParser()

	testCases := []struct {
		b        byte
		expected int
	}{
		{0x00, 1}, // ASCII
		{0x7F, 1}, // ASCII max
		{0xC0, 2}, // 2-byte start
		{0xDF, 2}, // 2-byte start
		{0xE0, 3}, // 3-byte start
		{0xEF, 3}, // 3-byte start
		{0xF0, 4}, // 4-byte start
		{0xF7, 4}, // 4-byte start
		{0x80, 1}, // Continuation byte (fallback)
	}

	for _, tc := range testCases {
		size := p.utf8CharSize(tc.b)
		if size != tc.expected {
			t.Errorf("utf8CharSize(0x%X) = %d, expected %d", tc.b, size, tc.expected)
		}
	}
}

func TestStreamingScenario(t *testing.T) {
	p := NewUTF8StreamParser()

	// Simulate streaming: "Hello, 世界! 🌍"
	chunks := [][]byte{
		[]byte("Hello, "),
		{0xE4, 0xB8}, // partial 世
		{0x96, 0xE7}, // complete 世, partial 界
		{0x95, 0x8C}, // complete 界
		[]byte("! "),
		{0xF0, 0x9F}, // partial 🌍
		{0x8C, 0x8D}, // complete 🌍
	}

	var results []string
	for _, chunk := range chunks {
		p.Write(chunk)
		if result, ok := p.Read(); ok {
			results = append(results, result)
		}
	}

	combined := strings.Join(results, "")
	if combined != "Hello, 世界! 🌍" {
		t.Errorf("expected 'Hello, 世界! 🌍', got '%s'", combined)
	}
}

func TestValidUTF8Output(t *testing.T) {
	p := NewUTF8StreamParser()

	testStrings := []string{
		"Hello World",
		"你好世界",
		"こんにちは",
		"🎉🎊🎁",
		"Mixed 混合 Текст ტექსტი",
	}

	for _, s := range testStrings {
		p.Reset()
		p.Write([]byte(s))
		result, ok := p.Read()
		if !ok {
			t.Errorf("expected ok for '%s'", s)
		}
		if !utf8.ValidString(result) {
			t.Errorf("invalid UTF-8 output for input '%s'", s)
		}
		if result != s {
			t.Errorf("expected '%s', got '%s'", s, result)
		}
	}
}

func TestLargeData(t *testing.T) {
	p := NewUTF8StreamParser()

	// Generate large UTF-8 string
	var builder strings.Builder
	for i := 0; i < 1000; i++ {
		builder.WriteString("Hello 世界! ")
	}
	largeString := builder.String()

	p.Write([]byte(largeString))
	result, ok := p.Read()
	if !ok {
		t.Error("expected ok for large data")
	}
	if result != largeString {
		t.Error("large data mismatch")
	}
}

func TestByteByByteWriting(t *testing.T) {
	p := NewUTF8StreamParser()
	input := "Hello 世界"
	inputBytes := []byte(input)

	var results []string
	for _, b := range inputBytes {
		p.Write([]byte{b})
		if result, ok := p.Read(); ok {
			results = append(results, result)
		}
	}

	combined := strings.Join(results, "")
	if combined != input {
		t.Errorf("expected '%s', got '%s'", input, combined)
	}
}

func TestEmoji4ByteUTF8(t *testing.T) {
	p := NewUTF8StreamParser()

	// 🎉 = F0 9F 8E 89
	emoji := "🎉"
	emojiBytes := []byte(emoji)

	for i := 0; i < len(emojiBytes)-1; i++ {
		p.Write(emojiBytes[i : i+1])
		result, ok := p.Read()
		if ok && result != "" {
			t.Errorf("unexpected output before emoji complete: '%s'", result)
		}
	}

	p.Write(emojiBytes[len(emojiBytes)-1:])
	result, ok := p.Read()
	if !ok {
		t.Error("expected ok after completing emoji")
	}
	if result != emoji {
		t.Errorf("expected '%s', got '%s'", emoji, result)
	}
}

func TestContinuationBytesOnly(t *testing.T) {
	p := NewUTF8StreamParser()

	// Write only continuation bytes (invalid UTF-8)
	p.Write([]byte{0x80, 0x80, 0x80})

	result, ok := p.Read()
	// Should handle gracefully - either return nothing or return the bytes
	_ = result
	_ = ok
}

func TestUTF8StreamParser_ConcurrentSafety(t *testing.T) {
	// Note: UTF8StreamParser doesn't have built-in locks,
	// so this test verifies it works with external synchronization
	p := NewUTF8StreamParser()
	var mu sync.Mutex
	const numGoroutines = 10
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				mu.Lock()
				switch j % 4 {
				case 0:
					p.Write([]byte("test"))
				case 1:
					p.Read()
				case 2:
					p.Flush()
				case 3:
					p.Reset()
				}
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
}

func TestConsecutiveReads(t *testing.T) {
	p := NewUTF8StreamParser()
	p.Write([]byte("hello"))

	result1, ok1 := p.Read()
	if !ok1 || result1 != "hello" {
		t.Error("first read failed")
	}

	result2, ok2 := p.Read()
	if ok2 || result2 != "" {
		t.Error("second read should return empty")
	}
}

func TestFlushThenWrite(t *testing.T) {
	p := NewUTF8StreamParser()
	p.Write([]byte("first"))
	p.Flush()
	p.Write([]byte("second"))

	result, ok := p.Read()
	if !ok || result != "second" {
		t.Errorf("expected 'second', got '%s'", result)
	}
}

func TestResetThenWrite(t *testing.T) {
	p := NewUTF8StreamParser()
	p.Write([]byte("first"))
	p.Reset()
	p.Write([]byte("second"))

	result, ok := p.Read()
	if !ok || result != "second" {
		t.Errorf("expected 'second', got '%s'", result)
	}
}
