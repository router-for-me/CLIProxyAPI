// dispatcher.go implements auth update dispatching and queue management.
// It batches, deduplicates, and delivers auth updates to registered consumers.
package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

var snapshotCoreAuthsFunc = snapshotCoreAuths

func (w *Watcher) setAuthUpdateQueue(queue chan<- AuthUpdate) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	w.authQueue = queue
	if w.dispatchCond == nil {
		w.dispatchCond = sync.NewCond(&w.dispatchMu)
	}
	if w.dispatchCancel != nil {
		w.dispatchCancel()
		if w.dispatchCond != nil {
			w.dispatchMu.Lock()
			w.dispatchCond.Broadcast()
			w.dispatchMu.Unlock()
		}
		w.dispatchCancel = nil
	}
	if queue != nil {
		ctx, cancel := context.WithCancel(context.Background())
		w.dispatchCancel = cancel
		go w.dispatchLoop(ctx)
	}
}

func (w *Watcher) dispatchRuntimeAuthUpdate(update AuthUpdate) bool {
	if w == nil {
		return false
	}
	w.clientsMutex.Lock()
	if w.runtimeAuths == nil {
		w.runtimeAuths = make(map[string]*coreauth.Auth)
	}
	switch update.Action {
	case AuthUpdateActionAdd, AuthUpdateActionModify:
		if update.Auth != nil && update.Auth.ID != "" {
			clone := update.Auth.Clone()
			w.runtimeAuths[clone.ID] = clone
			if w.currentAuths == nil {
				w.currentAuths = make(map[string]*coreauth.Auth)
			}
			if w.currentAuthHashes == nil {
				w.currentAuthHashes = make(map[string]string)
			}
			w.currentAuths[clone.ID] = clone.Clone()
			w.currentAuthHashes[clone.ID] = normalizeAuth(clone)
		}
	case AuthUpdateActionDelete:
		id := update.ID
		if id == "" && update.Auth != nil {
			id = update.Auth.ID
		}
		if id != "" {
			delete(w.runtimeAuths, id)
			if w.currentAuths != nil {
				delete(w.currentAuths, id)
			}
			if w.currentAuthHashes != nil {
				delete(w.currentAuthHashes, id)
			}
		}
	}
	w.clientsMutex.Unlock()
	if w.getAuthQueue() == nil {
		return false
	}
	w.dispatchAuthUpdates([]AuthUpdate{update})
	return true
}

func (w *Watcher) refreshAuthState(force bool) {
	w.clientsMutex.RLock()
	cfg := w.config
	authDir := w.effectiveAuthDir()
	w.clientsMutex.RUnlock()
	auths := snapshotCoreAuthsFunc(cfg, authDir)
	w.clientsMutex.Lock()
	if len(w.runtimeAuths) > 0 {
		for _, a := range w.runtimeAuths {
			if a != nil {
				auths = append(auths, a.Clone())
			}
		}
	}
	updates := w.prepareAuthUpdatesLocked(auths, force)
	w.clientsMutex.Unlock()
	w.dispatchAuthUpdates(updates)
}

func (w *Watcher) prepareAuthUpdatesLocked(auths []*coreauth.Auth, force bool) []AuthUpdate {
	newState := make(map[string]*coreauth.Auth, len(auths))
	newHashes := make(map[string]string, len(auths))
	for _, auth := range auths {
		if auth == nil || auth.ID == "" {
			continue
		}
		clone := auth.Clone()
		newState[auth.ID] = clone
		newHashes[auth.ID] = normalizeAuth(clone)
	}
	if w.currentAuths == nil {
		w.currentAuths = newState
		w.currentAuthHashes = newHashes
		if w.authQueue == nil {
			return nil
		}
		updates := make([]AuthUpdate, 0, len(newState))
		for id, auth := range newState {
			updates = append(updates, AuthUpdate{Action: AuthUpdateActionAdd, ID: id, Auth: auth.Clone()})
		}
		return updates
	}
	if w.authQueue == nil {
		w.currentAuths = newState
		w.currentAuthHashes = newHashes
		return nil
	}
	updates := make([]AuthUpdate, 0, len(newState)+len(w.currentAuths))
	for id, auth := range newState {
		if existing, ok := w.currentAuths[id]; !ok {
			updates = append(updates, AuthUpdate{Action: AuthUpdateActionAdd, ID: id, Auth: auth.Clone()})
		} else if force || w.currentAuthHashLocked(id, existing) != newHashes[id] {
			updates = append(updates, AuthUpdate{Action: AuthUpdateActionModify, ID: id, Auth: auth.Clone()})
		}
	}
	for id := range w.currentAuths {
		if _, ok := newState[id]; !ok {
			updates = append(updates, AuthUpdate{Action: AuthUpdateActionDelete, ID: id})
		}
	}
	w.currentAuths = newState
	w.currentAuthHashes = newHashes
	return updates
}

func (w *Watcher) dispatchAuthUpdates(updates []AuthUpdate) {
	if len(updates) == 0 {
		return
	}
	queue := w.getAuthQueue()
	if queue == nil {
		return
	}
	baseTS := time.Now().UnixNano()
	w.dispatchMu.Lock()
	if w.pendingUpdates == nil {
		w.pendingUpdates = make(map[string]AuthUpdate)
	}
	for idx, update := range updates {
		key := w.authUpdateKey(update, baseTS+int64(idx))
		if _, exists := w.pendingUpdates[key]; !exists {
			w.pendingOrder = append(w.pendingOrder, key)
		} else {
			w.dispatchMerged.Add(1)
		}
		w.pendingUpdates[key] = update
	}
	w.dispatchBacklog.Store(int64(len(w.pendingOrder)))
	if w.dispatchCond != nil {
		w.dispatchCond.Signal()
	}
	w.dispatchMu.Unlock()
}

func (w *Watcher) authUpdateKey(update AuthUpdate, ts int64) string {
	if update.ID != "" {
		return update.ID
	}
	return fmt.Sprintf("%s:%d", update.Action, ts)
}

func (w *Watcher) dispatchLoop(ctx context.Context) {
	for {
		batch, ok := w.nextPendingBatch(ctx)
		if !ok {
			return
		}
		if backlog := w.dispatchBacklog.Load(); backlog > 0 {
			log.Debugf("watcher dispatch backlog=%d merged=%d", backlog, w.dispatchMerged.Load())
		}
		queue := w.getAuthQueue()
		if queue == nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		for _, update := range batch {
			select {
			case queue <- update:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (w *Watcher) nextPendingBatch(ctx context.Context) ([]AuthUpdate, bool) {
	w.dispatchMu.Lock()
	defer w.dispatchMu.Unlock()
	for len(w.pendingOrder) == 0 {
		if ctx.Err() != nil {
			return nil, false
		}
		w.dispatchCond.Wait()
		if ctx.Err() != nil {
			return nil, false
		}
	}
	batch := make([]AuthUpdate, 0, len(w.pendingOrder))
	for _, key := range w.pendingOrder {
		batch = append(batch, w.pendingUpdates[key])
		delete(w.pendingUpdates, key)
	}
	w.pendingOrder = w.pendingOrder[:0]
	w.dispatchBacklog.Store(0)
	return batch, true
}

func (w *Watcher) getAuthQueue() chan<- AuthUpdate {
	w.clientsMutex.RLock()
	defer w.clientsMutex.RUnlock()
	return w.authQueue
}

func (w *Watcher) stopDispatch() {
	if w.dispatchCancel != nil {
		w.dispatchCancel()
		w.dispatchCancel = nil
	}
	w.dispatchMu.Lock()
	w.pendingOrder = nil
	w.pendingUpdates = nil
	w.dispatchBacklog.Store(0)
	if w.dispatchCond != nil {
		w.dispatchCond.Broadcast()
	}
	w.dispatchMu.Unlock()
	w.clientsMutex.Lock()
	w.authQueue = nil
	w.clientsMutex.Unlock()
}

func authEqual(a, b *coreauth.Auth) bool {
	return reflect.DeepEqual(normalizeAuth(a), normalizeAuth(b))
}

func normalizeAuth(a *coreauth.Auth) string {
	if a == nil {
		return ""
	}
	payload, errMarshal := json.Marshal(authFingerprintPayload(a))
	if errMarshal != nil {
		return fmt.Sprintf("%v", authFingerprintPayload(a))
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func authFingerprintPayload(a *coreauth.Auth) map[string]any {
	if a == nil {
		return nil
	}
	payload := map[string]any{
		"id":               a.ID,
		"provider":         a.Provider,
		"prefix":           a.Prefix,
		"file_name":        a.FileName,
		"label":            a.Label,
		"status":           a.Status,
		"status_message":   a.StatusMessage,
		"disabled":         a.Disabled,
		"unavailable":      a.Unavailable,
		"proxy_url":        a.ProxyURL,
		"attributes":       cloneStringMap(a.Attributes),
		"metadata":         fingerprintValue(a.Metadata),
		"quota":            authQuotaFingerprint(a.Quota),
		"last_error":       authErrorFingerprint(a.LastError),
		"next_retry_after": a.NextRetryAfter,
		"model_states":     authModelStatesFingerprint(a.ModelStates),
	}
	return payload
}

func authQuotaFingerprint(quota coreauth.QuotaState) map[string]any {
	return map[string]any{
		"exceeded":      quota.Exceeded,
		"reason":        quota.Reason,
		"backoff_level": quota.BackoffLevel,
	}
}

func authErrorFingerprint(err *coreauth.Error) map[string]any {
	if err == nil {
		return nil
	}
	return map[string]any{
		"code":        err.Code,
		"message":     err.Message,
		"retryable":   err.Retryable,
		"http_status": err.HTTPStatus,
	}
}

func authModelStatesFingerprint(states map[string]*coreauth.ModelState) map[string]any {
	if states == nil {
		return nil
	}
	out := make(map[string]any, len(states))
	for model, state := range states {
		if state == nil {
			out[model] = nil
			continue
		}
		out[model] = map[string]any{
			"status":           state.Status,
			"status_message":   state.StatusMessage,
			"unavailable":      state.Unavailable,
			"next_retry_after": state.NextRetryAfter,
			"last_error":       authErrorFingerprint(state.LastError),
			"quota":            authQuotaFingerprint(state.Quota),
		}
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func fingerprintValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return typed
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	case map[string]string:
		return cloneStringMap(typed)
	case map[string]any:
		if typed == nil {
			return nil
		}
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = fingerprintValue(item)
		}
		return out
	case []string:
		if typed == nil {
			return nil
		}
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	case []any:
		if typed == nil {
			return nil
		}
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, fingerprintValue(item))
		}
		return out
	}

	valueRef := reflect.ValueOf(value)
	if !valueRef.IsValid() {
		return nil
	}
	switch valueRef.Kind() {
	case reflect.Pointer, reflect.Interface:
		if valueRef.IsNil() {
			return nil
		}
		return fingerprintValue(valueRef.Elem().Interface())
	case reflect.Map:
		out := make(map[string]any, valueRef.Len())
		iter := valueRef.MapRange()
		for iter.Next() {
			out[fmt.Sprint(iter.Key().Interface())] = fingerprintValue(iter.Value().Interface())
		}
		return out
	case reflect.Slice, reflect.Array:
		out := make([]any, 0, valueRef.Len())
		for index := 0; index < valueRef.Len(); index++ {
			out = append(out, fingerprintValue(valueRef.Index(index).Interface()))
		}
		return out
	case reflect.Struct:
		payload, errMarshal := json.Marshal(value)
		if errMarshal == nil {
			var decoded any
			if errUnmarshal := json.Unmarshal(payload, &decoded); errUnmarshal == nil {
				return decoded
			}
		}
	}
	return fmt.Sprintf("%T:%v", value, value)
}

func (w *Watcher) currentAuthHashLocked(id string, auth *coreauth.Auth) string {
	if w == nil || id == "" {
		return ""
	}
	if w.currentAuthHashes == nil {
		w.currentAuthHashes = make(map[string]string)
	}
	if hash := w.currentAuthHashes[id]; hash != "" {
		return hash
	}
	if auth == nil {
		return ""
	}
	hash := normalizeAuth(auth)
	w.currentAuthHashes[id] = hash
	return hash
}

func snapshotCoreAuths(cfg *config.Config, authDir string) []*coreauth.Auth {
	ctx := &synthesizer.SynthesisContext{
		Config:      cfg,
		AuthDir:     authDir,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	}

	var out []*coreauth.Auth

	configSynth := synthesizer.NewConfigSynthesizer()
	if auths, err := configSynth.Synthesize(ctx); err == nil {
		out = append(out, auths...)
	}

	fileSynth := synthesizer.NewFileSynthesizer()
	if auths, err := fileSynth.Synthesize(ctx); err == nil {
		out = append(out, auths...)
	}

	return out
}
