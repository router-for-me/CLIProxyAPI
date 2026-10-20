package executor

import (
	"context"
	"encoding/json"
	"errors"
)

type boundModelKey struct{}

var ErrBoundModelChanged = errors.New("delegated_model_changed")

// WithBoundModel forbids payload rules from changing an admitted model.
// It is internal server context, never a caller-supplied header or boolean.
func WithBoundModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, boundModelKey{}, model)
}

// ValidateBoundModel is read-only and runs after the final payload barrier.
func ValidateBoundModel(ctx context.Context, body []byte) error {
	if ctx == nil {
		return nil
	}
	model, _ := ctx.Value(boundModelKey{}).(string)
	if model == "" {
		return nil
	}
	var payload struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Model != model {
		return ErrBoundModelChanged
	}
	return nil
}
