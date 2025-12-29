package quota

import (
	"context"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Worker handles periodic quota refresh in the background.
// It uses a time.Ticker to periodically fetch quota data for all configured
// providers and cache it in memory.
type Worker struct {
	manager  *Manager
	interval time.Duration
	mu       sync.Mutex
	running  bool
	stopCh   chan struct{}
}

// NewWorker creates a new quota refresh worker.
// The interval specifies how often the worker should refresh quota data.
// If interval is <= 0, the worker will not start when Start is called.
func NewWorker(manager *Manager, interval time.Duration) *Worker {
	return &Worker{
		manager:  manager,
		interval: interval,
	}
}

// Start begins the background refresh loop.
// Returns immediately if interval is <= 0 or if the worker is already running.
// The worker will perform an initial fetch immediately, then continue at the
// configured interval until Stop is called or the context is cancelled.
func (w *Worker) Start(ctx context.Context) {
	w.mu.Lock()
	if w.interval <= 0 || w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	w.stopCh = make(chan struct{})
	w.mu.Unlock()

	log.Infof("quota worker: starting with interval %v", w.interval)
	go w.run(ctx)
}

// Stop halts the background refresh loop.
// This method is safe to call multiple times or if the worker is not running.
func (w *Worker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.running {
		return
	}

	close(w.stopCh)
	w.running = false
}

// IsRunning returns true if the worker is currently running.
func (w *Worker) IsRunning() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}

// Interval returns the configured refresh interval.
func (w *Worker) Interval() time.Duration {
	return w.interval
}

func (w *Worker) run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	// Initial fetch on startup
	w.refresh(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Info("quota worker: shutting down due to context cancellation")
			w.mu.Lock()
			w.running = false
			w.mu.Unlock()
			return
		case <-w.stopCh:
			log.Info("quota worker: stopped")
			return
		case <-ticker.C:
			w.refresh(ctx)
		}
	}
}

func (w *Worker) refresh(ctx context.Context) {
	log.Debug("quota worker: starting refresh")

	// Use a timeout context to prevent hanging on slow API calls
	refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, err := w.manager.FetchAllQuotas(refreshCtx)
	if err != nil {
		log.Warnf("quota worker: refresh failed: %v", err)
		return
	}

	log.Debug("quota worker: refresh completed")
}
