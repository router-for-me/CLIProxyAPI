package errors

import (
	"encoding/json"
	"regexp"
	"testing"
)

var errorCodePattern = regexp.MustCompile(`^KP-(AUTH|PROV|CONF|NET|RATE|SYS)-\d{3}$`)

func TestErrorCodeFormat(t *testing.T) {
	tests := []struct {
		code  string
		valid bool
	}{
		{"KP-AUTH-001", true},
		{"KP-PROV-101", true},
		{"KP-CONF-201", true},
		{"KP-NET-301", true},
		{"KP-RATE-401", true},
		{"KP-SYS-501", true},
		{"KP-INVALID-001", false},
		{"AUTH-001", false},
		{"KP-AUTH-1", false},
		{"KP-AUTH-0001", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			if errorCodePattern.MatchString(tt.code) != tt.valid {
				t.Errorf("code %q: expected valid=%v", tt.code, tt.valid)
			}
		})
	}
}

func TestAllCategoriesHaveValidCodes(t *testing.T) {
	categories := []ErrorCategory{AUTH, PROV, CONF, NET, RATE, SYS}

	for _, cat := range categories {
		found := false
		for code := range ErrorRegistry {
			if getCategory(code) == cat {
				found = true
				if !errorCodePattern.MatchString(code) {
					t.Errorf("invalid code format in registry: %s", code)
				}
			}
		}
		if !found {
			t.Errorf("no codes found for category %s", cat)
		}
	}
}

func TestGetErrorReturnsCorrectMessages(t *testing.T) {
	tests := []struct {
		code            string
		expectedMessage string
	}{
		{"KP-AUTH-001", "Invalid credentials"},
		{"KP-AUTH-002", "Token expired"},
		{"KP-AUTH-003", "OAuth failed"},
		{"KP-PROV-101", "Provider unavailable"},
		{"KP-PROV-102", "Invalid response"},
		{"KP-PROV-103", "Unsupported model"},
		{"KP-CONF-201", "Invalid configuration"},
		{"KP-CONF-202", "Missing required field"},
		{"KP-CONF-203", "Schema validation failed"},
		{"KP-NET-301", "Connection refused"},
		{"KP-NET-302", "Request timeout"},
		{"KP-NET-303", "DNS resolution failed"},
		{"KP-RATE-401", "Rate limited"},
		{"KP-RATE-402", "Quota exceeded"},
		{"KP-RATE-403", "Concurrent request limit"},
		{"KP-SYS-501", "Internal error"},
		{"KP-SYS-502", "Out of memory"},
		{"KP-SYS-503", "Disk full"},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			err := GetError(tt.code)
			if err == nil {
				t.Fatalf("GetError(%s) returned nil", tt.code)
			}
			if err.Message != tt.expectedMessage {
				t.Errorf("expected message %q, got %q", tt.expectedMessage, err.Message)
			}
		})
	}
}

func TestGetErrorReturnsNilForUnknownCode(t *testing.T) {
	err := GetError("KP-AUTH-999")
	if err != nil {
		t.Errorf("expected nil for unknown code, got %v", err)
	}
}

func TestErrorIncludesSeverity(t *testing.T) {
	tests := []struct {
		code     string
		severity ErrorSeverity
	}{
		{"KP-AUTH-001", SeverityError},
		{"KP-PROV-103", SeverityWarning},
		{"KP-NET-302", SeverityWarning},
		{"KP-SYS-501", SeverityCritical},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			err := GetError(tt.code)
			if err == nil {
				t.Fatalf("GetError(%s) returned nil", tt.code)
			}
			if err.Severity != tt.severity {
				t.Errorf("expected severity %s, got %s", tt.severity, err.Severity)
			}
		})
	}
}

func TestErrorJSONSerialization(t *testing.T) {
	err := GetError("KP-AUTH-001")
	if err == nil {
		t.Fatal("GetError returned nil")
	}

	data, jsonErr := json.Marshal(err)
	if jsonErr != nil {
		t.Fatalf("failed to marshal error: %v", jsonErr)
	}

	var result map[string]interface{}
	if jsonErr := json.Unmarshal(data, &result); jsonErr != nil {
		t.Fatalf("failed to unmarshal error: %v", jsonErr)
	}

	requiredFields := []string{"code", "message", "description", "troubleshooting", "severity", "http_status"}
	for _, field := range requiredFields {
		if _, ok := result[field]; !ok {
			t.Errorf("missing field %q in JSON output", field)
		}
	}

	if result["code"] != "KP-AUTH-001" {
		t.Errorf("expected code KP-AUTH-001, got %v", result["code"])
	}

	troubleshooting, ok := result["troubleshooting"].([]interface{})
	if !ok {
		t.Error("troubleshooting is not an array")
	} else if len(troubleshooting) == 0 {
		t.Error("troubleshooting array is empty")
	}
}

func TestHTTPStatusCodeMapping(t *testing.T) {
	tests := []struct {
		code       string
		httpStatus int
	}{
		{"KP-AUTH-001", 401},
		{"KP-AUTH-002", 401},
		{"KP-AUTH-003", 401},
		{"KP-PROV-101", 502},
		{"KP-PROV-102", 502},
		{"KP-PROV-103", 502},
		{"KP-CONF-201", 400},
		{"KP-CONF-202", 400},
		{"KP-CONF-203", 400},
		{"KP-NET-301", 503},
		{"KP-NET-302", 503},
		{"KP-NET-303", 503},
		{"KP-RATE-401", 429},
		{"KP-RATE-402", 429},
		{"KP-RATE-403", 429},
		{"KP-SYS-501", 500},
		{"KP-SYS-502", 500},
		{"KP-SYS-503", 500},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			err := GetError(tt.code)
			if err == nil {
				t.Fatalf("GetError(%s) returned nil", tt.code)
			}
			if err.HTTPStatus != tt.httpStatus {
				t.Errorf("expected HTTP status %d, got %d", tt.httpStatus, err.HTTPStatus)
			}
		})
	}
}

func TestErrorInterface(t *testing.T) {
	err := GetError("KP-AUTH-001")
	if err == nil {
		t.Fatal("GetError returned nil")
	}

	var goErr error = err
	errStr := goErr.Error()
	expected := "KP-AUTH-001: Invalid credentials"
	if errStr != expected {
		t.Errorf("expected error string %q, got %q", expected, errStr)
	}
}

func TestFormatError(t *testing.T) {
	err := GetError("KP-AUTH-001")
	if err == nil {
		t.Fatal("GetError returned nil")
	}

	formatted := FormatError(err)
	expected := "[KP-AUTH-001] Invalid credentials: The provided credentials are invalid or have been revoked."
	if formatted != expected {
		t.Errorf("expected formatted error %q, got %q", expected, formatted)
	}
}

func TestNewError(t *testing.T) {
	err := NewError(AUTH, "001", "Test message")
	if err == nil {
		t.Fatal("NewError returned nil")
	}
	if err.Code != "KP-AUTH-001" {
		t.Errorf("expected code KP-AUTH-001, got %s", err.Code)
	}
	if err.Message != "Test message" {
		t.Errorf("expected message 'Test message', got %s", err.Message)
	}
}

func TestNewErrorFromRegistry(t *testing.T) {
	err := NewErrorFromRegistry("KP-AUTH-001")
	if err == nil {
		t.Fatal("NewErrorFromRegistry returned nil")
	}
	if err.Code != "KP-AUTH-001" {
		t.Errorf("expected code KP-AUTH-001, got %s", err.Code)
	}
}

func TestNilErrorString(t *testing.T) {
	var err *KorProxyError
	if err.Error() != "" {
		t.Errorf("expected empty string for nil error, got %s", err.Error())
	}
}

func getCategory(code string) ErrorCategory {
	if len(code) < 7 {
		return ""
	}
	switch code[3:7] {
	case "AUTH":
		return AUTH
	case "PROV":
		return PROV
	case "CONF":
		return CONF
	case "RATE":
		return RATE
	case "SYS-":
		return SYS
	}
	if len(code) >= 6 && code[3:6] == "NET" {
		return NET
	}
	if len(code) >= 6 && code[3:6] == "SYS" {
		return SYS
	}
	return ""
}
