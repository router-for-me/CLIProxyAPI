// Package refreshdiagnostic exposes safe OAuth refresh failure diagnostics.
package refreshdiagnostic

import (
	"encoding/json"
	"errors"
	"fmt"
)

const Code = "provider_reauthentication_required"
const Message = "The provider refresh token is no longer usable. Sign in again through OAuth Login."

type Error struct {
	Status       int
	Reason       string
	InvalidGrant bool
}

func (e *Error) Error() string {
	if e.InvalidGrant {
		return fmt.Sprintf("token refresh failed with status %d: invalid_grant", e.Status)
	}
	if e.Reason != "" {
		return fmt.Sprintf("token refresh failed with status %d: %s; %s", e.Status, e.Reason, Message)
	}
	return fmt.Sprintf("token refresh failed with status %d", e.Status)
}
func (e *Error) StatusCode() int { return e.Status }

func IsReason(reason string) bool {
	return reason == "refresh_token_expired" || reason == "refresh_token_reused"
}

// FromResponse deliberately discards upstream messages and unknown fields.
func FromResponse(status int, body []byte) error {
	var response struct {
		Code  string          `json:"code"`
		Error json.RawMessage `json:"error"`
	}
	result := &Error{Status: status}
	if json.Unmarshal(body, &response) == nil && (status == 400 || status == 401) {
		code := response.Code
		if code == "" {
			var nested struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(response.Error, &nested) == nil {
				code = nested.Code
			}
			if code == "" {
				_ = json.Unmarshal(response.Error, &code)
			}
		}
		result.InvalidGrant = code == "invalid_grant"
		if IsReason(code) {
			result.Reason = code
		}
	}
	return result
}

func Reason(err error) string {
	var diagnostic *Error
	if errors.As(err, &diagnostic) && diagnostic != nil && IsReason(diagnostic.Reason) {
		return diagnostic.Reason
	}
	return ""
}
