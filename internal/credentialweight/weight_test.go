package credentialweight

import (
	"encoding/json"
	"testing"
)

func TestParseValueValidation(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		want    int64
		wantErr bool
	}{
		{name: "default string", value: "", want: Default},
		{name: "negative excluded", value: json.Number("-5"), want: 0},
		{name: "fraction rejected", value: json.Number("1.5"), wantErr: true},
		{name: "maximum", value: json.Number("1000000"), want: Max},
		{name: "above maximum", value: json.Number("1000001"), wantErr: true},
		{name: "int64 overflow", value: json.Number("9223372036854775808"), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, errParse := ParseValue(test.value)
			if (errParse != nil) != test.wantErr {
				t.Fatalf("ParseValue(%v) error = %v, wantErr=%v", test.value, errParse, test.wantErr)
			}
			if !test.wantErr && got != test.want {
				t.Fatalf("ParseValue(%v) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}
