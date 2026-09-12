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
