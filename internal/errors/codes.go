package errors

import "fmt"

type ErrorCategory string

const (
	AUTH ErrorCategory = "AUTH"
	PROV ErrorCategory = "PROV"
	CONF ErrorCategory = "CONF"
	NET  ErrorCategory = "NET"
	RATE ErrorCategory = "RATE"
	SYS  ErrorCategory = "SYS"
)

type ErrorSeverity string

const (
	SeverityCritical ErrorSeverity = "critical"
	SeverityError    ErrorSeverity = "error"
	SeverityWarning  ErrorSeverity = "warning"
	SeverityInfo     ErrorSeverity = "info"
)

type KorProxyError struct {
	Code            string        `json:"code"`
	Message         string        `json:"message"`
	Description     string        `json:"description"`
	Troubleshooting []string      `json:"troubleshooting"`
	Severity        ErrorSeverity `json:"severity"`
	HTTPStatus      int           `json:"http_status"`
}

func (e *KorProxyError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

func (e *KorProxyError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.HTTPStatus
}

var ErrorRegistry = map[string]*KorProxyError{
	"KP-AUTH-001": {
		Code:        "KP-AUTH-001",
		Message:     "Invalid credentials",
		Description: "The provided credentials are invalid or have been revoked.",
		Troubleshooting: []string{
			"Verify your account credentials are correct",
			"Re-authenticate with the provider",
			"Check if your subscription is still active",
		},
		Severity:   SeverityError,
		HTTPStatus: 401,
	},
	"KP-AUTH-002": {
		Code:        "KP-AUTH-002",
		Message:     "Token expired",
		Description: "The authentication token has expired and needs to be refreshed.",
		Troubleshooting: []string{
			"Click the refresh button to renew your session",
			"Re-authenticate if refresh fails",
			"Check your system clock is accurate",
		},
		Severity:   SeverityError,
		HTTPStatus: 401,
	},
	"KP-AUTH-003": {
		Code:        "KP-AUTH-003",
		Message:     "OAuth failed",
		Description: "The OAuth authentication flow failed to complete.",
		Troubleshooting: []string{
			"Ensure you completed the authentication in your browser",
			"Check your internet connection",
			"Try disabling browser extensions that may interfere",
			"Clear browser cookies and try again",
		},
		Severity:   SeverityError,
		HTTPStatus: 401,
	},
	"KP-PROV-101": {
		Code:        "KP-PROV-101",
		Message:     "Provider unavailable",
		Description: "The AI provider service is currently unavailable.",
		Troubleshooting: []string{
			"Check the provider status page for outages",
			"Wait a few minutes and try again",
			"Switch to an alternate provider if available",
		},
		Severity:   SeverityError,
		HTTPStatus: 502,
	},
	"KP-PROV-102": {
		Code:        "KP-PROV-102",
		Message:     "Invalid response",
		Description: "The provider returned an unexpected or malformed response.",
		Troubleshooting: []string{
			"Retry the request",
			"Check if the model ID is correct",
			"Report this issue if it persists",
		},
		Severity:   SeverityError,
		HTTPStatus: 502,
	},
	"KP-PROV-103": {
		Code:        "KP-PROV-103",
		Message:     "Unsupported model",
		Description: "The requested model is not supported by the provider or your subscription.",
		Troubleshooting: []string{
			"Check available models in your subscription",
			"Verify the model ID spelling",
			"Upgrade your subscription if required",
		},
		Severity:   SeverityWarning,
		HTTPStatus: 502,
	},
	"KP-CONF-201": {
		Code:        "KP-CONF-201",
		Message:     "Invalid configuration",
		Description: "The configuration file contains invalid settings.",
		Troubleshooting: []string{
			"Review recent configuration changes",
			"Reset to default configuration",
			"Check configuration file syntax",
		},
		Severity:   SeverityError,
		HTTPStatus: 400,
	},
	"KP-CONF-202": {
		Code:        "KP-CONF-202",
		Message:     "Missing required field",
		Description: "A required configuration field is missing.",
		Troubleshooting: []string{
			"Check the error details for the missing field name",
			"Add the required field to your configuration",
			"Refer to documentation for required fields",
		},
		Severity:   SeverityError,
		HTTPStatus: 400,
	},
	"KP-CONF-203": {
		Code:        "KP-CONF-203",
		Message:     "Schema validation failed",
		Description: "The configuration does not match the expected schema.",
		Troubleshooting: []string{
			"Validate your configuration against the schema",
			"Check field types match expected types",
			"Remove any unknown or deprecated fields",
		},
		Severity:   SeverityError,
		HTTPStatus: 400,
	},
	"KP-NET-301": {
		Code:        "KP-NET-301",
		Message:     "Connection refused",
		Description: "The connection to the server was refused.",
		Troubleshooting: []string{
			"Check if the proxy server is running",
			"Verify the port number is correct",
			"Check firewall settings",
		},
		Severity:   SeverityError,
		HTTPStatus: 503,
	},
	"KP-NET-302": {
		Code:        "KP-NET-302",
		Message:     "Request timeout",
		Description: "The request timed out waiting for a response.",
		Troubleshooting: []string{
			"Check your internet connection",
			"The provider may be experiencing high load",
			"Try again with a simpler request",
		},
		Severity:   SeverityWarning,
		HTTPStatus: 503,
	},
	"KP-NET-303": {
		Code:        "KP-NET-303",
		Message:     "DNS resolution failed",
		Description: "Unable to resolve the server hostname.",
		Troubleshooting: []string{
			"Check your internet connection",
			"Verify DNS settings",
			"Try using a different DNS server",
		},
		Severity:   SeverityError,
		HTTPStatus: 503,
	},
	"KP-RATE-401": {
		Code:        "KP-RATE-401",
		Message:     "Rate limited",
		Description: "You have exceeded the rate limit for requests.",
		Troubleshooting: []string{
			"Wait before making more requests",
			"Reduce request frequency",
			"Check your subscription rate limits",
		},
		Severity:   SeverityWarning,
		HTTPStatus: 429,
	},
	"KP-RATE-402": {
		Code:        "KP-RATE-402",
		Message:     "Quota exceeded",
		Description: "You have exceeded your usage quota.",
		Troubleshooting: []string{
			"Check your subscription usage",
			"Wait for quota reset (usually monthly)",
			"Upgrade your subscription for higher limits",
		},
		Severity:   SeverityWarning,
		HTTPStatus: 429,
	},
	"KP-RATE-403": {
		Code:        "KP-RATE-403",
		Message:     "Concurrent request limit",
		Description: "Too many concurrent requests in progress.",
		Troubleshooting: []string{
			"Wait for pending requests to complete",
			"Reduce parallelism in your application",
			"Queue requests to avoid concurrent limits",
		},
		Severity:   SeverityWarning,
		HTTPStatus: 429,
	},
	"KP-SYS-501": {
		Code:        "KP-SYS-501",
		Message:     "Internal error",
		Description: "An unexpected internal error occurred.",
		Troubleshooting: []string{
			"Restart the KorProxy application",
			"Check application logs for details",
			"Report this issue with logs attached",
		},
		Severity:   SeverityCritical,
		HTTPStatus: 500,
	},
	"KP-SYS-502": {
		Code:        "KP-SYS-502",
		Message:     "Out of memory",
		Description: "The application has run out of available memory.",
		Troubleshooting: []string{
			"Close other applications to free memory",
			"Restart the KorProxy application",
			"Increase system memory if issue persists",
		},
		Severity:   SeverityCritical,
		HTTPStatus: 500,
	},
	"KP-SYS-503": {
		Code:        "KP-SYS-503",
		Message:     "Disk full",
		Description: "Insufficient disk space available.",
		Troubleshooting: []string{
			"Free up disk space",
			"Clear application cache and logs",
			"Move data to another drive",
		},
		Severity:   SeverityCritical,
		HTTPStatus: 500,
	},
}

func GetError(code string) *KorProxyError {
	if err, ok := ErrorRegistry[code]; ok {
		return err
	}
	return nil
}

func FormatError(err *KorProxyError) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("[%s] %s: %s", err.Code, err.Message, err.Description)
}

func NewError(category ErrorCategory, number string, message string) *KorProxyError {
	code := fmt.Sprintf("KP-%s-%s", category, number)
	return &KorProxyError{
		Code:            code,
		Message:         message,
		Description:     message,
		Troubleshooting: []string{},
		Severity:        SeverityError,
		HTTPStatus:      500,
	}
}

func NewErrorFromRegistry(code string) *KorProxyError {
	if err := GetError(code); err != nil {
		copied := *err
		return &copied
	}
	return nil
}

func GetErrorsByCategory(category ErrorCategory) []*KorProxyError {
	var result []*KorProxyError
	prefix := fmt.Sprintf("KP-%s-", category)
	for code, err := range ErrorRegistry {
		if len(code) >= len(prefix) && code[:len(prefix)] == prefix {
			result = append(result, err)
		}
	}
	return result
}

func GetErrorsBySeverity(severity ErrorSeverity) []*KorProxyError {
	var result []*KorProxyError
	for _, err := range ErrorRegistry {
		if err.Severity == severity {
			result = append(result, err)
		}
	}
	return result
}
