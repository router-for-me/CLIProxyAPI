package usage

import (
	"context"
	"sync/atomic"

	"github.com/google/uuid"
)

type scopeKey struct{}
type generationKey struct{}
type AccountingScope struct{ published atomic.Bool }

func WithAccountingScope(ctx context.Context) (context.Context, *AccountingScope) {
	s := &AccountingScope{}
	return context.WithValue(ctx, scopeKey{}, s), s
}
func (s *AccountingScope) Published() bool { return s != nil && s.published.Load() }
func WithNewGeneration(ctx context.Context) context.Context {
	return context.WithValue(ctx, generationKey{}, uuid.NewString())
}
func GenerationFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(generationKey{}).(string)
	return id
}
func markPublished(ctx context.Context) {
	if ctx != nil {
		if s, ok := ctx.Value(scopeKey{}).(*AccountingScope); ok {
			s.published.Store(true)
		}
	}
}

// WithAccountingContext carries accounting identity across an execution context
// boundary without changing its cancellation or copying unrelated request values.
// An explicitly chosen execution generation takes precedence over the request.
func WithAccountingContext(target, source context.Context) context.Context {
	if target == nil {
		target = context.Background()
	}
	if source == nil {
		return target
	}
	if target.Value(scopeKey{}) == nil {
		if scope, ok := source.Value(scopeKey{}).(*AccountingScope); ok {
			target = context.WithValue(target, scopeKey{}, scope)
		}
	}
	if GenerationFromContext(target) == "" {
		if id := GenerationFromContext(source); id != "" {
			target = context.WithValue(target, generationKey{}, id)
		}
	}
	return target
}
