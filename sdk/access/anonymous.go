package access

import (
	"context"
	"net/http"
)

// NewAnonymousProvider constructs a provider that explicitly allows requests.
func NewAnonymousProvider(name string) Provider {
	return anonymousProvider{name: name}
}

type anonymousProvider struct {
	name string
}

func (p anonymousProvider) Identifier() string {
	if p.name == "" {
		return "anonymous"
	}
	return p.name
}

func (p anonymousProvider) Authenticate(_ context.Context, _ *http.Request) (*Result, *AuthError) {
	return &Result{
		Provider: p.Identifier(),
		Metadata: map[string]string{
			"source": "anonymous",
		},
	}, nil
}
