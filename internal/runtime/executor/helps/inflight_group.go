package helps

import (
	"context"
	"fmt"

	"golang.org/x/sync/singleflight"
)

type InFlightGroup[T any] struct {
	group singleflight.Group
}

func (g *InFlightGroup[T]) Do(ctx context.Context, key string, fn func() (T, error)) (value T, executed bool, shared bool, err error) {
	if key == "" {
		value, err = fn()
		return value, true, false, err
	}

	var ran bool
	resultCh := g.group.DoChan(key, func() (any, error) {
		ran = true
		return fn()
	})

	select {
	case <-ctx.Done():
		var zero T
		return zero, false, false, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			var zero T
			return zero, ran, result.Shared, result.Err
		}
		typed, ok := result.Val.(T)
		if !ok {
			var zero T
			return zero, ran, result.Shared, fmt.Errorf("in-flight group: unexpected result type %T", result.Val)
		}
		return typed, ran, result.Shared, nil
	}
}
