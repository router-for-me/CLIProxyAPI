package usage

import "context"

// HomeSelection identifies executions associated with a Home dispatch. Dispatch
// charges the first execution; reused executions are charged by primary usage.
// Additional-model records are billing details, not extra scheduling requests.
// Model is the scheduling route, not the upstream model.
type HomeSelection struct {
	Reused           bool   `json:"reused"`
	Additional       bool   `json:"additional,omitempty"`
	Model            string `json:"model"`
	CredentialPolicy string `json:"credential_policy,omitempty"`
}

type homeSelectionContextKey struct{}

// WithHomeSelection captures immutable, attempt-scoped Home accounting metadata.
func WithHomeSelection(ctx context.Context, selection HomeSelection) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, homeSelectionContextKey{}, selection)
}

// HomeSelectionFromContext returns a copy so follow-up responses cannot change
// the accounting metadata retained by the first response's usage reporter.
func HomeSelectionFromContext(ctx context.Context) *HomeSelection {
	if ctx == nil {
		return nil
	}
	selection, ok := ctx.Value(homeSelectionContextKey{}).(HomeSelection)
	if !ok {
		return nil
	}
	return &selection
}
