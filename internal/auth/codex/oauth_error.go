package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// sanitizedOAuthIOError preserves trusted cancellation classes without wrapping
// arbitrary transport/body errors, whose messages may contain credentials.
func sanitizedOAuthIOError(message string, err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New(message)
}

// sanitizedOAuthError deliberately excludes descriptions and unknown provider
// fields. OAuth responses may echo credentials; even error codes are untrusted.
func sanitizedOAuthError(operation string, status int, body []byte) error {
	var response struct {
		Error json.RawMessage `json:"error"`
		Code  string          `json:"code"`
	}
	_ = json.Unmarshal(body, &response)
	var flatError string
	var nestedError struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(response.Error, &flatError)
	_ = json.Unmarshal(response.Error, &nestedError)
	code := "oauth_error"
	for _, candidate := range []string{response.Code, flatError, nestedError.Code} {
		switch candidate {
		case "refresh_token_reused", "invalid_grant", "invalid_client", "invalid_request", "unauthorized_client", "unsupported_grant_type", "invalid_scope", "temporarily_unavailable", "server_error":
			code = candidate
		}
		if code == "refresh_token_reused" {
			break
		}
	}
	return fmt.Errorf("%s failed with status %d: %s", operation, status, code)
}
