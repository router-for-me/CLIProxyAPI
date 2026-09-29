package dto

// ReasoningConversionState carries provider-native reasoning controls between
// in-process conversion steps. It is not part of any provider wire protocol;
// request fields that reference it must use json:"-".
//
// Converters that rebuild an OpenAI request must copy this state so exact
// budgets and explicit include-thoughts choices survive multi-step routes.
type ReasoningConversionState struct {
	Mode            string
	Effort          string
	BudgetTokens    *int
	IncludeThoughts *bool
}
