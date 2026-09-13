package helps

import (
	"errors"
	"net/http"
)

type copilotQuotaError struct{ error }

func (e copilotQuotaError) StatusCode() int          { return http.StatusTooManyRequests }
func (e copilotQuotaError) IsCredentialScoped() bool { return true }
func (e copilotQuotaError) Unwrap() error            { return e.error }

// CopilotQuotaError sends exhausted Copilot allowances through the usual cooldown
// and account failover path. Policy denials (403) retain their original meaning.
func CopilotQuotaError(err error) error {
	var status interface{ StatusCode() int }
	if errors.As(err, &status) && status.StatusCode() == http.StatusPaymentRequired {
		return copilotQuotaError{err}
	}
	return err
}
