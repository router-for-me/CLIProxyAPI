package auth

import (
	"fmt"
	"testing"
)

func TestIsTransientBadRequest_ChineseModelUnavailable(t *testing.T) {
	err := fmt.Errorf(`{"error":{"message":"当前模型暂时无法使用,请稍后再试","type":"<nil>"},"requestId":"1775821625034351"}`)
	if !isTransientBadRequest(err) {
		t.Fatal("expected Chinese 'model temporarily unavailable' message to be detected as transient")
	}
}

func TestIsTransientBadRequest_EnglishTemporarilyUnavailable(t *testing.T) {
	err := fmt.Errorf("model is temporarily unavailable, please try again later")
	if !isTransientBadRequest(err) {
		t.Fatal("expected English 'temporarily unavailable' message to be detected as transient")
	}
}

func TestIsTransientBadRequest_TryAgainLater(t *testing.T) {
	err := fmt.Errorf("service error: try again later")
	if !isTransientBadRequest(err) {
		t.Fatal("expected 'try again later' message to be detected as transient")
	}
}

func TestIsTransientBadRequest_RealInvalidRequest(t *testing.T) {
	err := fmt.Errorf(`{"error":{"message":"invalid_request_error: max_tokens must be positive","type":"invalid_request_error"}}`)
	if isTransientBadRequest(err) {
		t.Fatal("real invalid_request_error should NOT be detected as transient")
	}
}

func TestIsTransientBadRequest_Nil(t *testing.T) {
	if isTransientBadRequest(nil) {
		t.Fatal("nil error should not be transient")
	}
}

