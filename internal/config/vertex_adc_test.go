package config

import (
	"strings"
	"testing"
)

func TestValidateVertexADC(t *testing.T) {
	tests := []struct {
		name    string
		entries []VertexADCConfig
		wantErr string
	}{
		{name: "empty config"},
		{name: "valid entry", entries: []VertexADCConfig{{ProjectID: "proj-1", Location: "global"}}},
		{name: "missing project-id", entries: []VertexADCConfig{{Location: "global"}}, wantErr: "project-id is required"},
		{name: "prefix with slash", entries: []VertexADCConfig{{ProjectID: "proj-1", Prefix: "a/b"}}, wantErr: "single segment"},
		{name: "second entry malformed", entries: []VertexADCConfig{{ProjectID: "proj-1"}, {ProjectID: " "}}, wantErr: "vertex-adc[1]"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (&Config{VertexADC: test.entries}).ValidateVertexADC()
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateVertexADC() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidateVertexADC() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestVertexADCLoadValidation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{name: "valid", yaml: "vertex-adc:\n  - project-id: proj-1\n    location: global\n"},
		{name: "missing project-id", yaml: "vertex-adc:\n  - location: global\n", wantErr: "project-id is required"},
		{name: "prefix with slash", yaml: "vertex-adc:\n  - project-id: proj-1\n    prefix: a/b\n", wantErr: "single segment"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseConfigBytes([]byte(test.yaml))
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("ParseConfigBytes() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ParseConfigBytes() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestVertexADCWeightValidation(t *testing.T) {
	tests := []struct {
		name   string
		weight string
		valid  bool
	}{
		{name: "maximum", weight: "1000000", valid: true},
		{name: "above maximum", weight: "1000001", valid: false},
		{name: "fraction", weight: "1.5", valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			yaml := "vertex-adc:\n  - project-id: proj-1\n    weight: " + test.weight + "\n"
			_, errParse := ParseConfigBytes([]byte(yaml))
			if (errParse == nil) != test.valid {
				t.Fatalf("ParseConfigBytes(weight=%s) error = %v, want valid=%v", test.weight, errParse, test.valid)
			}
		})
	}

	over := 1000001
	if err := (&Config{VertexADC: []VertexADCConfig{{ProjectID: "proj-1", Weight: &over}}}).ValidateCredentialWeights(); err == nil || !strings.Contains(err.Error(), "vertex-adc[0].weight") {
		t.Fatalf("ValidateCredentialWeights() error = %v, want vertex-adc[0].weight failure", err)
	}
}
