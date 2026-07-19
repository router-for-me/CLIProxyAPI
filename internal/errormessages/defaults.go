package errormessages

import "net/http"

// Default returns the built-in curated message for a status code. Used as
// the fallback when no override is configured and when PG is not available
// (file-only deployments). Operators can reset a row to its default by
// DELETE-ing it via the dashboard.
func Default(code int) Message {
	base := Message{StatusCode: code, Enabled: true}
	switch code {
	case http.StatusBadRequest:
		base.Title = "Bad Request"
		base.Message = "The request was malformed or missing required fields."
	case http.StatusUnauthorized:
		base.Title = "Unauthorized"
		base.Message = "Authentication is required and has failed or has not been provided."
	case http.StatusForbidden:
		base.Title = "Forbidden"
		base.Message = "You do not have permission to access this resource."
	case http.StatusNotFound:
		base.Title = "Not Found"
		base.Message = "The requested resource was not found."
	case http.StatusMethodNotAllowed:
		base.Title = "Method Not Allowed"
		base.Message = "The HTTP method is not allowed for this resource."
	case http.StatusRequestTimeout:
		base.Title = "Request Timeout"
		base.Message = "The server timed out waiting for the request."
	case http.StatusConflict:
		base.Title = "Conflict"
		base.Message = "The request could not be completed due to a conflict with the current state of the target resource."
	case http.StatusPaymentRequired:
		base.Title = "Payment Required"
		base.Message = "The API key budget has been exceeded. {{details}}"
	case http.StatusTooManyRequests:
		base.Title = "Too Many Requests"
		base.Message = "Rate limit exceeded: {{details}}"
	case http.StatusInternalServerError:
		base.Title = "Internal Server Error"
		base.Message = "The server encountered an unexpected condition."
	case http.StatusBadGateway:
		base.Title = "Bad Gateway"
		base.Message = "The upstream provider returned an invalid response."
	case http.StatusServiceUnavailable:
		base.Title = "Service Unavailable"
		base.Message = "The server is currently unable to handle this request. {{details}}"
	case http.StatusGatewayTimeout:
		base.Title = "Gateway Timeout"
		base.Message = "The upstream provider did not respond in time."
	default:
		if code >= 400 && code < 500 {
			base.Title = "Client Error"
			base.Message = "The request could not be processed."
		} else if code >= 500 {
			base.Title = "Server Error"
			base.Message = "The server encountered an error."
		} else {
			base.Title = "OK"
			base.Message = ""
			base.Enabled = false
		}
	}
	return base
}

// KnownCodes lists the status codes the dashboard renders as editable
// presets. Any other 4xx/5xx code is still editable via the "Custom code"
// field, but this list populates the default table on first load.
func KnownCodes() []int {
	return []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusMethodNotAllowed,
		http.StatusRequestTimeout,
		http.StatusConflict,
		http.StatusPaymentRequired,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	}
}
