package watcher

import (
	"context"
	"fmt"

	log "github.com/sirupsen/logrus"
)

type configCompletion interface {
	Start(context.Context, func(), func(error), func()) error
	Close() error
}

func (w *Watcher) notifyConfigComplete() {
	if w == nil || w.stopped.Load() {
		return
	}
	w.reloadConfigIfChanged()
}

func (w *Watcher) configCompletionUnavailable(err error) {
	if w == nil || w.stopped.Load() {
		return
	}
	if w.completionActive.Swap(false) {
		log.WithError(err).Error("config completion watcher unavailable; using fsnotify debounce fallback")
		w.reloadConfigIfChanged()
	}
}

func configCompletionError(operation string, err error) error {
	return fmt.Errorf("config completion watcher %s: %w", operation, err)
}
