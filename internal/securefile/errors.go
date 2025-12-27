package securefile

// LoadError captures best-effort load failures (parse/decrypt/read) when scanning auth stores.
type LoadError struct {
	Path      string `json:"path"`
	ErrorType string `json:"error_type"`
	Message   string `json:"message"`
}
