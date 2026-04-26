package util

import (
	"errors"
	"strings"
	"testing"
)

func TestReadResponseBodyLimitedRejectsOversizedBody(t *testing.T) {
	_, err := ReadResponseBodyLimited(strings.NewReader("abcdef"), 5)
	if !errors.Is(err, ErrResponseBodyTooLarge) {
		t.Fatalf("ReadResponseBodyLimited() error = %v, want ErrResponseBodyTooLarge", err)
	}
}

func TestReadResponseBodyLimitedAllowsBodyAtLimit(t *testing.T) {
	body, err := ReadResponseBodyLimited(strings.NewReader("abcde"), 5)
	if err != nil {
		t.Fatalf("ReadResponseBodyLimited() error = %v", err)
	}
	if string(body) != "abcde" {
		t.Fatalf("ReadResponseBodyLimited() = %q, want %q", string(body), "abcde")
	}
}
