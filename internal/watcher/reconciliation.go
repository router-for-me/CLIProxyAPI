package watcher

import "time"

const authReconcileDebounce = 100 * time.Millisecond

func (w *Watcher) scheduleAuthReconciliation() {
	if w == nil || w.stopped.Load() {
		return
	}
	w.authReconcileMu.Lock()
	if w.authReconcileTimer != nil {
		w.authReconcileTimer.Stop()
	}
	w.authReconcileTimer = time.AfterFunc(authReconcileDebounce, func() {
		if w.stopped.Load() {
			return
		}
		w.refreshAuthState(false)
	})
	w.authReconcileMu.Unlock()
}

func (w *Watcher) stopAuthReconciliation() {
	if w == nil {
		return
	}
	w.authReconcileMu.Lock()
	if w.authReconcileTimer != nil {
		w.authReconcileTimer.Stop()
		w.authReconcileTimer = nil
	}
	w.authReconcileMu.Unlock()
}
