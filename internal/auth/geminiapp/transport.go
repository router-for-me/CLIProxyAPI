package geminiapp

// transportError hides page tokens and proxy credentials in transport messages
// while retaining typed causes for cancellation and retry classification.
type transportError struct {
	operation string
	cause     error
}

func (e transportError) Error() string { return "gemini-app: " + e.operation + " failed" }
func (e transportError) Unwrap() error { return e.cause }

// NewTransportError keeps upstream request URLs out of usage, logs and API errors.
func NewTransportError(operation string, cause error) error {
	return transportError{operation: operation, cause: cause}
}
