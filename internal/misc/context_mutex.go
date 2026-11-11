package misc

import (
	"context"
	"sync"

	"golang.org/x/sync/semaphore"
)

// ContextMutex lets credential acquisition stop waiting behind a stalled store
// operation. Its zero value is ready to use and must not be copied after use.
type ContextMutex struct {
	once sync.Once
	sem  *semaphore.Weighted
}

func (m *ContextMutex) LockContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.once.Do(func() { m.sem = semaphore.NewWeighted(1) })
	return m.sem.Acquire(ctx, 1)
}

func (m *ContextMutex) Lock() {
	_ = m.LockContext(context.Background())
}

func (m *ContextMutex) Unlock() {
	m.sem.Release(1)
}
