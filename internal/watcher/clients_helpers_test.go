package watcher

import "time"

// removeClient drops a removed auth file's auths, as a remove event would. Tests
// call it after removing the file, so the current time follows the removal.
func (w *Watcher) removeClient(path string) {
	w.authRescanMu.Lock()
	defer w.authRescanMu.Unlock()

	w.removeClientLocked(path, time.Now())
}
