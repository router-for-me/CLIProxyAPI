package copilot

import (
	"fmt"
	"strings"
)

// AccountType is the Copilot subscription type.
type AccountType string

const (
	AccountTypeIndividual AccountType = "individual"
	AccountTypeBusiness   AccountType = "business"
	AccountTypeEnterprise AccountType = "enterprise"
)

// ValidAccountTypes is the canonical list of valid Copilot account types.
var ValidAccountTypes = []string{string(AccountTypeIndividual), string(AccountTypeBusiness), string(AccountTypeEnterprise)}

const DefaultAccountType = AccountTypeIndividual

// AccountTypeValidationResult contains the result of account type validation.
type AccountTypeValidationResult struct {
	AccountType  AccountType
	Valid        bool
	ValidValues  []string
	DefaultValue string
	ErrorMessage string
}

// ParseAccountType parses a string into an AccountType.
// Returns the parsed type and whether the input was a valid account type.
// Empty or invalid strings return (AccountTypeIndividual, false).
func ParseAccountType(s string) (AccountType, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "individual":
		return AccountTypeIndividual, true
	case "business":
		return AccountTypeBusiness, true
	case "enterprise":
		return AccountTypeEnterprise, true
	default:
		return AccountTypeIndividual, false
	}
}

// ValidateAccountType validates an account type string and returns details suitable for API responses.
func ValidateAccountType(s string) AccountTypeValidationResult {
	accountType, valid := ParseAccountType(s)
	result := AccountTypeValidationResult{
		AccountType:  accountType,
		Valid:        valid,
		ValidValues:  ValidAccountTypes,
		DefaultValue: string(DefaultAccountType),
	}
	if !valid && s != "" {
		result.ErrorMessage = fmt.Sprintf("invalid account_type '%s', valid values are: %s", s, strings.Join(ValidAccountTypes, ", "))
	}
	return result
}
