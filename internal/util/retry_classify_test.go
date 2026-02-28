package util

import (
	"errors"
	"testing"
)

func TestIsNonRetryableRefreshError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "refresh token reused", err: errors.New("refresh_token_reused"), want: true},
		{name: "invalid grant", err: errors.New("oauth INVALID_GRANT"), want: true},
		{name: "token invalidated", err: errors.New("Token_Invalidated by server"), want: true},
		{name: "other", err: errors.New("temporary network error"), want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsNonRetryableRefreshError(tc.err); got != tc.want {
				t.Fatalf("IsNonRetryableRefreshError()=%v, want %v", got, tc.want)
			}
		})
	}
}
