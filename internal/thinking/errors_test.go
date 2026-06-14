package thinking

import (
	"net/http"
	"testing"
)

func TestThinkingError(t *testing.T) {
	t.Run("Error returns message", func(t *testing.T) {
		err := NewThinkingError(ErrInvalidSuffix, "invalid suffix format")
		if err.Error() != "invalid suffix format" {
			t.Errorf("Error() = %q, want %q", err.Error(), "invalid suffix format")
		}
	})

	t.Run("StatusCode returns 400", func(t *testing.T) {
		err := NewThinkingError(ErrBudgetOutOfRange, "budget out of range")
		if got := err.StatusCode(); got != http.StatusBadRequest {
			t.Errorf("StatusCode() = %d, want %d", got, http.StatusBadRequest)
		}
	})

	t.Run("ErrorCode preservation", func(t *testing.T) {
		err := NewThinkingError(ErrThinkingNotSupported, "thinking not supported")
		if err.Code != ErrThinkingNotSupported {
			t.Errorf("Code = %q, want %q", err.Code, ErrThinkingNotSupported)
		}
	})
}

func TestNewThinkingError(t *testing.T) {
	err := NewThinkingError(ErrInvalidSuffix, "invalid suffix format")
	if err.Code != ErrInvalidSuffix {
		t.Errorf("Code = %q, want %q", err.Code, ErrInvalidSuffix)
	}
	if err.Message != "invalid suffix format" {
		t.Errorf("Message = %q, want %q", err.Message, "invalid suffix format")
	}
	if err.Model != "" {
		t.Errorf("Model = %q, want empty", err.Model)
	}
	if err.Details != nil {
		t.Errorf("Details = %v, want nil", err.Details)
	}
}

func TestNewThinkingErrorWithModel(t *testing.T) {
	err := NewThinkingErrorWithModel(ErrLevelNotSupported, "level not supported", "test-model")
	if err.Code != ErrLevelNotSupported {
		t.Errorf("Code = %q, want %q", err.Code, ErrLevelNotSupported)
	}
	if err.Message != "level not supported" {
		t.Errorf("Message = %q, want %q", err.Message, "level not supported")
	}
	if err.Model != "test-model" {
		t.Errorf("Model = %q, want %q", err.Model, "test-model")
	}
}

func TestErrorCodeValues(t *testing.T) {
	tests := []struct {
		code ErrorCode
		want string
	}{
		{ErrInvalidSuffix, "INVALID_SUFFIX"},
		{ErrUnknownLevel, "UNKNOWN_LEVEL"},
		{ErrThinkingNotSupported, "THINKING_NOT_SUPPORTED"},
		{ErrLevelNotSupported, "LEVEL_NOT_SUPPORTED"},
		{ErrBudgetOutOfRange, "BUDGET_OUT_OF_RANGE"},
		{ErrProviderMismatch, "PROVIDER_MISMATCH"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if string(tt.code) != tt.want {
				t.Errorf("ErrorCode = %q, want %q", tt.code, tt.want)
			}
		})
	}
}

func TestThinkingErrorImplementsError(t *testing.T) {
	var err error = NewThinkingError(ErrInvalidSuffix, "test error")
	if err.Error() != "test error" {
		t.Errorf("error.Error() = %q, want %q", err.Error(), "test error")
	}
}
