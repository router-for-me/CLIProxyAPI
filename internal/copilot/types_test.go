 package copilot
 
 import (
 	"testing"
 )
 
 func TestParseAccountType(t *testing.T) {
 	tests := []struct {
 		name      string
 		input     string
 		wantType  AccountType
 		wantValid bool
 	}{
 		{
 			name:      "individual lowercase",
 			input:     "individual",
 			wantType:  AccountTypeIndividual,
 			wantValid: true,
 		},
 		{
 			name:      "individual uppercase",
 			input:     "INDIVIDUAL",
 			wantType:  AccountTypeIndividual,
 			wantValid: true,
 		},
 		{
 			name:      "individual mixed case",
 			input:     "Individual",
 			wantType:  AccountTypeIndividual,
 			wantValid: true,
 		},
 		{
 			name:      "business",
 			input:     "business",
 			wantType:  AccountTypeBusiness,
 			wantValid: true,
 		},
 		{
 			name:      "enterprise",
 			input:     "enterprise",
 			wantType:  AccountTypeEnterprise,
 			wantValid: true,
 		},
 		{
 			name:      "empty string",
 			input:     "",
 			wantType:  AccountTypeIndividual,
 			wantValid: false,
 		},
 		{
 			name:      "invalid value",
 			input:     "invalid",
 			wantType:  AccountTypeIndividual,
 			wantValid: false,
 		},
 		{
 			name:      "whitespace",
 			input:     "  individual  ",
 			wantType:  AccountTypeIndividual,
 			wantValid: true,
 		},
 	}
 
 	for _, tt := range tests {
 		t.Run(tt.name, func(t *testing.T) {
 			gotType, gotValid := ParseAccountType(tt.input)
 			if gotType != tt.wantType {
 				t.Errorf("ParseAccountType(%q) type = %v, want %v", tt.input, gotType, tt.wantType)
 			}
 			if gotValid != tt.wantValid {
 				t.Errorf("ParseAccountType(%q) valid = %v, want %v", tt.input, gotValid, tt.wantValid)
 			}
 		})
 	}
 }
 
 func TestValidateAccountType(t *testing.T) {
 	tests := []struct {
 		name           string
 		input          string
 		wantValid      bool
 		wantHasError   bool
 		wantType       AccountType
 	}{
 		{
 			name:         "valid individual",
 			input:        "individual",
 			wantValid:    true,
 			wantHasError: false,
 			wantType:     AccountTypeIndividual,
 		},
 		{
 			name:         "valid business",
 			input:        "business",
 			wantValid:    true,
 			wantHasError: false,
 			wantType:     AccountTypeBusiness,
 		},
 		{
 			name:         "valid enterprise",
 			input:        "enterprise",
 			wantValid:    true,
 			wantHasError: false,
 			wantType:     AccountTypeEnterprise,
 		},
 		{
 			name:         "invalid value",
 			input:        "invalid",
 			wantValid:    false,
 			wantHasError: true,
 			wantType:     AccountTypeIndividual,
 		},
 		{
 			name:         "empty string",
 			input:        "",
 			wantValid:    false,
 			wantHasError: false, // empty string doesn't generate error message
 			wantType:     AccountTypeIndividual,
 		},
 	}
 
 	for _, tt := range tests {
 		t.Run(tt.name, func(t *testing.T) {
 			result := ValidateAccountType(tt.input)
 			if result.Valid != tt.wantValid {
 				t.Errorf("ValidateAccountType(%q).Valid = %v, want %v", tt.input, result.Valid, tt.wantValid)
 			}
 			if (result.ErrorMessage != "") != tt.wantHasError {
 				t.Errorf("ValidateAccountType(%q).ErrorMessage = %q, wantHasError %v", tt.input, result.ErrorMessage, tt.wantHasError)
 			}
 			if result.AccountType != tt.wantType {
 				t.Errorf("ValidateAccountType(%q).AccountType = %v, want %v", tt.input, result.AccountType, tt.wantType)
 			}
 			if result.DefaultValue != string(DefaultAccountType) {
 				t.Errorf("ValidateAccountType(%q).DefaultValue = %q, want %q", tt.input, result.DefaultValue, DefaultAccountType)
 			}
 			if len(result.ValidValues) != 3 {
 				t.Errorf("ValidateAccountType(%q).ValidValues has %d items, want 3", tt.input, len(result.ValidValues))
 			}
 		})
 	}
 }
 
 func TestAccountTypeConstants(t *testing.T) {
 	if AccountTypeIndividual != "individual" {
 		t.Errorf("AccountTypeIndividual = %q, want %q", AccountTypeIndividual, "individual")
 	}
 	if AccountTypeBusiness != "business" {
 		t.Errorf("AccountTypeBusiness = %q, want %q", AccountTypeBusiness, "business")
 	}
 	if AccountTypeEnterprise != "enterprise" {
 		t.Errorf("AccountTypeEnterprise = %q, want %q", AccountTypeEnterprise, "enterprise")
 	}
 	if DefaultAccountType != AccountTypeIndividual {
 		t.Errorf("DefaultAccountType = %q, want %q", DefaultAccountType, AccountTypeIndividual)
 	}
 }
 
 func TestValidAccountTypes(t *testing.T) {
 	expected := []string{"individual", "business", "enterprise"}
 	if len(ValidAccountTypes) != len(expected) {
 		t.Fatalf("ValidAccountTypes has %d items, want %d", len(ValidAccountTypes), len(expected))
 	}
 	for i, v := range expected {
 		if ValidAccountTypes[i] != v {
 			t.Errorf("ValidAccountTypes[%d] = %q, want %q", i, ValidAccountTypes[i], v)
 		}
 	}
 }
