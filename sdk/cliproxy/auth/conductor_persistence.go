package auth

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"

	log "github.com/sirupsen/logrus"
)

// lockAuthMutation acquires the reload barrier, then the credential gate, before
// m.mu. Keep both until publication completes. Load never takes credential or
// refresh locks. Store callbacks may read the manager, but must not mutate it.
func (m *Manager) lockAuthMutation(id string) func() {
	release, _ := m.lockAuthMutationContext(context.Background(), id)
	return release
}

type metadataPersistPreemptState struct {
	mu       sync.Mutex
	waiters  int
	fgActive int
	fgIdle   chan struct{}
	cancel   context.CancelFunc
	done     chan struct{}
}

type metadataPersistContextKey struct{}

func withMetadataPersistContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, metadataPersistContextKey{}, true)
}

func isMetadataPersistContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(metadataPersistContextKey{}).(bool)
	return v
}

func (m *Manager) metadataPersistPreempt(id string) *metadataPersistPreemptState {
	val, _ := m.metadataPersistCancels.LoadOrStore(id, &metadataPersistPreemptState{})
	return val.(*metadataPersistPreemptState)
}

func (m *Manager) globalMetadataSaveState() *metadataPersistPreemptState {
	val, _ := m.metadataSaveGate.LoadOrStore("state", &metadataPersistPreemptState{})
	return val.(*metadataPersistPreemptState)
}

func (m *Manager) acquireMetadataSaveGate(ctx context.Context, cancel context.CancelFunc) (func(), bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	val, _ := m.metadataSaveGate.LoadOrStore("gate", make(chan struct{}, 1))
	gate := val.(chan struct{})
	select {
	case gate <- struct{}{}:
		if ctx.Err() != nil {
			<-gate
			return nil, false
		}
	case <-ctx.Done():
		return nil, false
	}

	gState := m.globalMetadataSaveState()
	for {
		gState.mu.Lock()
		if ctx.Err() != nil {
			gState.mu.Unlock()
			<-gate
			return nil, false
		}
		if gState.fgActive == 0 {
			done := make(chan struct{})
			gState.done = done
			gState.cancel = cancel
			gState.mu.Unlock()
			var once sync.Once
			release := func() {
				once.Do(func() {
					gState.mu.Lock()
					gState.cancel = nil
					gState.done = nil
					gState.mu.Unlock()
					close(done)
					<-gate
				})
			}
			return release, true
		}
		fgIdle := gState.fgIdle
		gState.mu.Unlock()
		select {
		case <-fgIdle:
		case <-ctx.Done():
			<-gate
			return nil, false
		}
	}
}

func (m *Manager) beginForegroundStoreSave(ctx context.Context) (func(), error) {
	if m == nil || isMetadataPersistContext(ctx) {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	gState := m.globalMetadataSaveState()
	gState.mu.Lock()
	if gState.fgActive == 0 {
		gState.fgIdle = make(chan struct{})
	}
	gState.fgActive++
	done := gState.done
	cancel := gState.cancel
	gState.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			gState.mu.Lock()
			gState.fgActive--
			if gState.fgActive == 0 && gState.fgIdle != nil {
				close(gState.fgIdle)
				gState.fgIdle = nil
			}
			gState.mu.Unlock()
		})
	}
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
			if errCtx := ctx.Err(); errCtx != nil {
				release()
				return nil, errCtx
			}
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		}
	} else if errCtx := ctx.Err(); errCtx != nil {
		release()
		return nil, errCtx
	}
	return release, nil
}

// lockAuthMutationContext lets fallible mutations cancel either lock wait.
func (m *Manager) lockAuthMutationContext(ctx context.Context, id string) (func(), error) {
	return m.lockAuthMutationWithPersistCancel(ctx, id, nil)
}

func (m *Manager) lockAuthMutationWithPersistCancel(ctx context.Context, id string, persistCancel context.CancelFunc) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if errAcquire := m.authLoadGate.Acquire(ctx, 1); errAcquire != nil {
		return nil, errAcquire
	}
	value, _ := m.authMutationLocks.LoadOrStore(id, make(chan struct{}, 1))
	gate := value.(chan struct{})
	state := m.metadataPersistPreempt(id)
	if persistCancel == nil {
		state.mu.Lock()
		state.waiters++
		cancel := state.cancel
		state.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		defer func() {
			state.mu.Lock()
			state.waiters--
			state.mu.Unlock()
		}()
	}
	select {
	case gate <- struct{}{}:
		// Cancellation may race with the gate becoming available.
		if errContext := ctx.Err(); errContext != nil {
			<-gate
			m.authLoadGate.Release(1)
			return nil, errContext
		}
	case <-ctx.Done():
		m.authLoadGate.Release(1)
		return nil, ctx.Err()
	}
	if persistCancel != nil {
		state.mu.Lock()
		if state.waiters > 0 {
			state.mu.Unlock()
			persistCancel()
		} else {
			state.cancel = persistCancel
			state.mu.Unlock()
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if persistCancel != nil {
				state.mu.Lock()
				state.cancel = nil
				state.mu.Unlock()
			}
			<-gate
			m.authLoadGate.Release(1)
		})
	}, nil
}

// persistLocked requires m.mu and the credential mutation gate. Never pass a
// published object to Store.Save: readers and runtime-only writers remain live
// during I/O. Only the store's delta is merged back, preserving intervening edits.
func (m *Manager) persistLocked(ctx context.Context, auth *Auth) error {
	before := auth.Clone()
	saved := auth.Clone()
	m.mu.Unlock()
	err := m.persist(ctx, saved)
	m.mu.Lock()
	mergeAuthSaveDelta(auth, before, saved, true)
	return err
}

// mergeAuthSaveDelta handles all exported value fields, not a store-specific
// allowlist. Map entries are independent fields; nested values are atomic.
// Identity/version fields belong exclusively to the manager. Store-owned shared
// objects must be replaced rather than mutated in place (see Store.Save).
func mergeAuthSaveDelta(target, before, after *Auth, preserveConcurrent bool) {
	dst := reflect.ValueOf(target).Elem()
	base := reflect.ValueOf(before).Elem()
	next := reflect.ValueOf(after).Elem()
	for i := 0; i < dst.NumField(); i++ {
		field := dst.Type().Field(i)
		if field.PkgPath != "" || field.Name == "ID" || field.Name == "RegistrationEpoch" || field.Name == "CredentialVersion" || field.Name == "Generation" || field.Name == "RejectedAccessToken" || field.Name == "RejectedSIWCAccessToken" {
			continue
		}
		d, b, n := dst.Field(i), base.Field(i), next.Field(i)
		if reflect.DeepEqual(b.Interface(), n.Interface()) {
			continue
		}
		if reflect.DeepEqual(d.Interface(), b.Interface()) {
			d.Set(n)
			continue
		}
		if d.Kind() == reflect.Map {
			keys := make(map[any]reflect.Value)
			for _, key := range b.MapKeys() {
				keys[key.Interface()] = key
			}
			for _, key := range n.MapKeys() {
				keys[key.Interface()] = key
			}
			for _, key := range keys {
				oldValue, newValue := b.MapIndex(key), n.MapIndex(key)
				if equalSaveValue(oldValue, newValue) || (preserveConcurrent && !equalSaveValue(d.MapIndex(key), oldValue)) {
					continue
				}
				if d.IsNil() {
					d.Set(reflect.MakeMap(d.Type()))
				}
				d.SetMapIndex(key, newValue)
			}
			continue
		}
		if !preserveConcurrent {
			d.Set(n)
		}
	}
}

func equalSaveValue(a, b reflect.Value) bool {
	if !a.IsValid() || !b.IsValid() {
		return a.IsValid() == b.IsValid()
	}
	return reflect.DeepEqual(a.Interface(), b.Interface())
}

// PersistMetadataEntry updates a single metadata entry on the runtime auth for
// the given registration epoch and persists it to the backing store without
// advancing Generation or firing credential-lifecycle hooks. If a foreground
// credential mutation contends for the same auth while store I/O is in flight,
// the metadata save is preempted so foreground operations do not stall.
func (m *Manager) PersistMetadataEntry(ctx context.Context, id string, expectedEpoch uint64, key string, value any, validate func(existing *Auth) bool) (bool, bool) {
	if m == nil || id == "" || key == "" {
		return false, false
	}
	ctx = WithoutSkipPersist(ctx)
	if errContext := ctx.Err(); errContext != nil {
		return false, false
	}
	m.mu.RLock()
	existing := m.auths[id]
	if existing == nil || (expectedEpoch != 0 && existing.RegistrationEpoch != expectedEpoch) || (validate != nil && !validate(existing)) {
		m.mu.RUnlock()
		return false, false
	}
	m.mu.RUnlock()

	persistCtx, cancelPersist := context.WithCancel(withMetadataPersistContext(ctx))
	defer cancelPersist()
	var preempted atomic.Bool
	preemptFn := func() {
		preempted.Store(true)
		cancelPersist()
	}
	releaseMutation, errMutation := m.lockAuthMutationWithPersistCancel(persistCtx, id, preemptFn)
	if errMutation != nil {
		return false, preempted.Load() && ctx.Err() == nil
	}
	defer releaseMutation()

	releaseSaveGate, okGate := m.acquireMetadataSaveGate(persistCtx, preemptFn)
	if !okGate {
		return false, preempted.Load() && ctx.Err() == nil
	}
	defer releaseSaveGate()

	m.mu.Lock()
	existing = m.auths[id]
	if existing == nil || (expectedEpoch != 0 && existing.RegistrationEpoch != expectedEpoch) || (validate != nil && !validate(existing)) {
		m.mu.Unlock()
		return false, false
	}
	if preempted.Load() {
		m.mu.Unlock()
		return false, ctx.Err() == nil
	}
	existingBeforeSave := existing.Clone()
	updated := existing.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata[key] = value
	errPersist := m.persistLocked(persistCtx, updated)
	if errPersist != nil {
		if !preempted.Load() {
			log.WithFields(log.Fields{"auth_id": updated.ID, "credential": updated.ID, "provider": updated.Provider}).Warnf("failed to persist metadata for auth %s (%s): %v", updated.Provider, updated.ID, errPersist)
		}
		m.mu.Unlock()
		return false, preempted.Load() && ctx.Err() == nil
	}
	current := m.auths[id]
	if current == nil || current.RegistrationEpoch != updated.RegistrationEpoch {
		m.mu.Unlock()
		return false, false
	}
	if validate != nil && !validate(current) {
		if current.Metadata != nil {
			invalidated := current.Clone()
			delete(invalidated.Metadata, key)
			m.auths[id] = invalidated
		}
		m.mu.Unlock()
		return false, false
	}
	mergeAuthSaveDelta(updated, existingBeforeSave, current, false)
	if current.Generation > updated.Generation {
		updated.Generation = current.Generation
	}
	m.auths[id] = updated.Clone()
	m.mu.Unlock()
	return true, false
}
