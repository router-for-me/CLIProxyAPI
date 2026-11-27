package notify

import (
	"context"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/router"
	log "github.com/sirupsen/logrus"
)

const maxLog = 100

// LogEntry records one delivery attempt for the dashboard.
type LogEntry struct {
	At        time.Time `json:"at"`
	Title     string    `json:"title"`
	Level     string    `json:"level"`
	Delivered bool      `json:"delivered"`
	Error     string    `json:"error,omitempty"`
}

// Notifier evaluates alerts and delivers them in the background.
type Notifier struct {
	Sender    Sender
	Evaluator *Evaluator

	mu    sync.Mutex
	cfg   Config
	log   []LogEntry
	queue chan Message
	once  sync.Once
	stop  chan struct{}
	wg    sync.WaitGroup
}

// NewNotifier returns a notifier with an empty alert state.
func NewNotifier(cfg Config) *Notifier {
	return &Notifier{cfg: cfg, Evaluator: NewEvaluator(), queue: make(chan Message, 64), stop: make(chan struct{})}
}

// Configure replaces settings.
func (n *Notifier) Configure(cfg Config) {
	n.mu.Lock()
	n.cfg = cfg
	n.mu.Unlock()
}

func (n *Notifier) config() Config {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.cfg
}

// Evaluate checks accounts for alerts and queues them. Alert state advances
// even with no channel configured, so enabling a channel later does not
// replay old alerts.
func (n *Notifier) Evaluate(accounts []router.AccountView, now time.Time) {
	cfg := n.config()
	for _, m := range n.Evaluator.Evaluate(cfg, accounts, now) {
		n.Enqueue(m)
	}
}

// Enqueue queues m for delivery when a channel is configured.
func (n *Notifier) Enqueue(m Message) {
	if !n.config().Enabled() {
		return
	}
	n.once.Do(n.start)
	select {
	case n.queue <- m:
	default:
		log.Warn("prox notify: queue full, dropping notification")
	}
}

func (n *Notifier) start() {
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		for {
			select {
			case <-n.stop:
				return
			case m := <-n.queue:
				n.deliver(m)
			}
		}
	}()
}

func (n *Notifier) deliver(m Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("prox notify: panic: %v", r)
		}
	}()
	cfg := n.config()
	delivered, errs := n.Sender.Send(context.Background(), cfg, m)
	entry := LogEntry{At: time.Now(), Title: m.Title, Level: m.Level, Delivered: delivered}
	if len(errs) > 0 {
		entry.Error = errs[0].Error()
		for _, err := range errs {
			log.WithError(err).Warn("prox notify: delivery failed")
		}
	}
	n.mu.Lock()
	n.log = append(n.log, entry)
	if len(n.log) > maxLog {
		n.log = append([]LogEntry(nil), n.log[len(n.log)-maxLog:]...)
	}
	n.mu.Unlock()
}

// Log returns recent deliveries, newest first.
func (n *Notifier) Log() []LogEntry {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]LogEntry, 0, len(n.log))
	for i := len(n.log) - 1; i >= 0; i-- {
		out = append(out, n.log[i])
	}
	return out
}

// Close stops the delivery worker. Queued messages are dropped.
func (n *Notifier) Close() {
	select {
	case <-n.stop:
	default:
		close(n.stop)
	}
	n.wg.Wait()
}

// Configured reports whether a delivery channel is set.
func (n *Notifier) Configured() bool { return n.config().Enabled() }

// ForwardIgnition reports whether ignition problems should be notified.
func (n *Notifier) ForwardIgnition() bool {
	cfg := n.config()
	return cfg.Enabled() && cfg.IgnitionEvents
}
