package helps

import (
	"errors"
	"strings"
	"testing"
)

func TestReadResponseBodyLimitedReturnsBodyWithinLimit(t *testing.T) {
	got, err := ReadResponseBodyLimited(strings.NewReader("hello"), 5)
	if err != nil {
		t.Fatalf("ReadResponseBodyLimited() error = %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("ReadResponseBodyLimited() = %q, want %q", string(got), "hello")
	}
}

func TestReadResponseBodyLimitedRejectsOversizedBody(t *testing.T) {
	_, err := ReadResponseBodyLimited(strings.NewReader("hello!"), 5)
	if !errors.Is(err, ErrResponseBodyTooLarge) {
		t.Fatalf("ReadResponseBodyLimited() error = %v, want ErrResponseBodyTooLarge", err)
	}
}
