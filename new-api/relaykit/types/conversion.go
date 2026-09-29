package types

import (
	"fmt"
	"strings"
)

type ConversionDiagnosticSeverity string

const (
	ConversionDiagnosticWarning ConversionDiagnosticSeverity = "warning"
	ConversionDiagnosticError   ConversionDiagnosticSeverity = "error"
)

type ConversionDiagnostic struct {
	Code     string                       `json:"code"`
	Path     string                       `json:"path,omitempty"`
	Message  string                       `json:"message"`
	Severity ConversionDiagnosticSeverity `json:"severity"`
	From     RelayFormat                  `json:"from"`
	To       RelayFormat                  `json:"to"`
}

type ConversionLossPolicy string

const (
	// ConversionLossPolicySafe rejects request-phase conversions that would
	// change tool execution semantics, while returning non-fatal loss as
	// diagnostics. It is opt-in; the default is ConversionLossPolicyAllow.
	ConversionLossPolicySafe ConversionLossPolicy = "safe"
	// ConversionLossPolicyStrict rejects every lossy conversion, including
	// presentation-only metadata loss.
	ConversionLossPolicyStrict ConversionLossPolicy = "strict"
	// ConversionLossPolicyAllow is the default. It permits lossy conversion
	// and reports every loss through the conversion result.
	ConversionLossPolicyAllow ConversionLossPolicy = "allow"
)

type ConversionLossError struct {
	Diagnostics []ConversionDiagnostic
}

func (e *ConversionLossError) Error() string {
	if e == nil || len(e.Diagnostics) == 0 {
		return "conversion would lose protocol semantics"
	}
	messages := make([]string, 0, len(e.Diagnostics))
	for _, diagnostic := range e.Diagnostics {
		message := diagnostic.Message
		if message == "" {
			message = diagnostic.Code
		}
		if diagnostic.Path != "" {
			message = fmt.Sprintf("%s: %s", diagnostic.Path, message)
		}
		messages = append(messages, message)
	}
	return "conversion would lose protocol semantics: " + strings.Join(messages, "; ")
}

func RejectConversionLoss(policy ConversionLossPolicy, diagnostics []ConversionDiagnostic) error {
	if policy == ConversionLossPolicyAllow || len(diagnostics) == 0 {
		return nil
	}
	rejected := make([]ConversionDiagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		if policy == ConversionLossPolicyStrict || diagnostic.Severity == ConversionDiagnosticError {
			rejected = append(rejected, diagnostic)
		}
	}
	if len(rejected) == 0 {
		return nil
	}
	return &ConversionLossError{Diagnostics: rejected}
}
