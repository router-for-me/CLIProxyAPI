package auth

import (
	"reflect"
	"strings"
	"testing"
)

// TestSanitizeErrorTextFieldsCoversAllStringFields ensures every exported string
// field of *Error is redacted by sanitizeErrorTextFields. If a new string field
// is added and not handled, this test fails because that field still contains
// the injected secret.
func TestSanitizeErrorTextFieldsCoversAllStringFields(t *testing.T) {
	const secret = "sk-live-1234567890"

	e := &Error{}
	v := reflect.ValueOf(e).Elem()
	stringFieldCount := 0
	for i := 0; i < v.NumField(); i++ {
		field := v.Type().Field(i)
		if !field.IsExported() || v.Field(i).Kind() != reflect.String {
			continue
		}
		stringFieldCount++
		v.Field(i).SetString(secret)
	}
	if stringFieldCount == 0 {
		t.Fatal("*Error has no string fields to sanitize")
	}

	sanitizeErrorTextFields(e)

	sv := reflect.ValueOf(e).Elem()
	for i := 0; i < sv.NumField(); i++ {
		field := sv.Type().Field(i)
		if !field.IsExported() || sv.Field(i).Kind() != reflect.String {
			continue
		}
		got := sv.Field(i).String()
		if strings.Contains(got, secret) {
			t.Fatalf("field %q not sanitized: %q", field.Name, got)
		}
		// If the field was a secret-bearing value, redaction should leave a marker.
		if got != "" && !strings.Contains(got, "REDACTED") {
			t.Fatalf("field %q did not contain redaction marker: %q", field.Name, got)
		}
	}
}
