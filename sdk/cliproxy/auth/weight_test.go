package auth

import (
	"encoding/json"
	"testing"
)

func TestValidateAuthWeight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		auth    *Auth
		wantErr bool
	}{
		{name: "omitted", auth: &Auth{}},
		{name: "positive attribute", auth: &Auth{Attributes: map[string]string{AttributeWeight: "7"}}},
		{name: "zero metadata", auth: &Auth{Metadata: map[string]any{AttributeWeight: json.Number("0")}}},
		{name: "negative attribute", auth: &Auth{Attributes: map[string]string{AttributeWeight: "-2"}}},
		{name: "fraction metadata", auth: &Auth{Metadata: map[string]any{AttributeWeight: json.Number("1.5")}}, wantErr: true},
		{name: "above maximum attribute", auth: &Auth{Attributes: map[string]string{AttributeWeight: "1000001"}}, wantErr: true},
		{name: "overflow metadata", auth: &Auth{Metadata: map[string]any{AttributeWeight: json.Number("9223372036854775808")}}, wantErr: true},
		{name: "nonnumeric attribute", auth: &Auth{Attributes: map[string]string{AttributeWeight: "invalid"}}, wantErr: true},
		{
			name: "valid attribute does not hide invalid metadata",
			auth: &Auth{
				Attributes: map[string]string{AttributeWeight: "2"},
				Metadata:   map[string]any{AttributeWeight: 1.5},
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			errValidate := ValidateAuthWeight(test.auth)
			if (errValidate != nil) != test.wantErr {
				t.Fatalf("ValidateAuthWeight() error = %v, wantErr = %v", errValidate, test.wantErr)
			}
		})
	}
}
