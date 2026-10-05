package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
	bolt "go.etcd.io/bbolt"
)

const MaxAccountingEventBytes = 64 * 1024

var ErrOutboxFull = errors.New("accounting outbox capacity reached")
var eventsBucket = []byte("events-v1")
var deliveriesBucket = []byte("deliveries-v1")

type DeliveryState string

const (
	DeliveryPending      DeliveryState = "pending"
	DeliveryAcknowledged DeliveryState = "acknowledged"
	DeliveryRetryable    DeliveryState = "retryable"
	DeliveryAmbiguous    DeliveryState = "ambiguous"
	DeliveryRejected     DeliveryState = "permanently_rejected"
)

// Delivery is separate from the immutable destination-independent event.
// Claim commits ambiguous before external I/O, so a crash cannot imply a safe retry.
type Delivery struct {
	State         DeliveryState `json:"state"`
	Attempts      uint64        `json:"attempts"`
	UpdatedAt     time.Time     `json:"updated_at"`
	NextAttemptAt time.Time     `json:"next_attempt_at,omitempty"`
}

type OutboxStats struct {
	Backlog                 int    `json:"backlog"`
	OldestPendingAgeSeconds int64  `json:"oldest_pending_age_seconds"`
	PersistenceFailures     uint64 `json:"persistence_failures"`
	DroppedEvents           uint64 `json:"dropped_events"`
	QueueDepth              int    `json:"queue_depth"`
}

type Outbox struct {
	prices      *PriceBook
	db          *bolt.DB
	unavailable bool
	cfg         config.AccountingOutboxConfig
	now         func() time.Time
	queue       chan []byte
	done        chan struct{}
	closeDone   chan struct{}
	mu          sync.Mutex
	closed      bool
	finalMu     sync.RWMutex
	finalStats  OutboxStats
	failures    atomic.Uint64
	dropped     atomic.Uint64
}

func OpenOutbox(cfg config.AccountingOutboxConfig) (*Outbox, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.Defaults()
	if cfg.DataPath == "" {
		return nil, errors.New("accounting data path is required")
	}
	if err := os.MkdirAll(cfg.DataPath, 0700); err != nil {
		return nil, fmt.Errorf("create accounting directory: %w", err)
	}
	db, err := openAccountingDB(filepath.Join(cfg.DataPath, "accounting.db"))
	if err != nil {
		return nil, fmt.Errorf("open accounting store (one process per path): %w", err)
	}
	db.AllocSize = 64 * 1024
	o := &Outbox{db: db, cfg: cfg, now: time.Now, queue: make(chan []byte, cfg.QueueCapacity), done: make(chan struct{}), closeDone: make(chan struct{})}
	err = o.view(validateOutbox)
	if err == nil {
		err = o.update(func(tx *bolt.Tx) error {
			for _, name := range [][]byte{eventsBucket, deliveriesBucket} {
				if _, err := tx.CreateBucketIfNotExists(name); err != nil {
					return err
				}
			}
			return nil
		})
	}

	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize accounting store: %w", err)
	}
	o.prices = loadPriceBook(cfg.Pricing, cfg.DataPath)
	go o.run()
	return o, nil
}

// HandleAccountingEvent only queues a bounded serialized snapshot. It is not a durable acknowledgement.
func (o *Outbox) HandleAccountingEvent(event AccountingEvent) {
	if o.unavailable {
		o.dropped.Add(1)
		return
	}
	if event.Estimate == nil {
		event.Estimate = o.prices.Estimate(event)
	}
	if !boundedAccountingEvent(event) {
		o.dropped.Add(1)
		return
	}
	raw, err := json.Marshal(event)
	if err != nil || len(raw) > MaxAccountingEventBytes || len(event.ExecutionID) == 0 || len(event.ExecutionID) > 512 || event.SchemaVersion != AccountingEventSchemaVersion {
		o.dropped.Add(1)
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		o.dropped.Add(1)
		return
	}
	select {
	case o.queue <- raw:
	default:
		o.dropped.Add(1)
	}
}

func (o *Outbox) run() {
	defer close(o.done)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case raw, ok := <-o.queue:
			if !ok {
				return
			}
			if err := o.insert(raw); err != nil {
				failures := o.failures.Add(1)
				o.dropped.Add(1)
				if failures == 1 {
					log.WithError(err).Warn("accounting persistence degraded")
				}
			}
		case <-ticker.C:
			if err := o.pruneAvailableStore(); err != nil {
				o.failures.Add(1)
				log.WithError(err).Warn("accounting retention failed")
			}
			stats, err := o.Stats()
			if err != nil {
				o.failures.Add(1)
				log.WithError(err).Warn("accounting status failed")
				continue
			}
			log.WithFields(log.Fields{"backlog": stats.Backlog, "oldest_pending_age_seconds": stats.OldestPendingAgeSeconds, "persistence_failures": stats.PersistenceFailures, "dropped_events": stats.DroppedEvents, "queue_depth": stats.QueueDepth}).Info("accounting outbox status")
		}
	}
}

// Insert returns success only after bbolt's transaction and fsync complete.
func (o *Outbox) Insert(event AccountingEvent) error {
	if event.Estimate == nil {
		event.Estimate = o.prices.Estimate(event)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return o.insert(raw)
}

func (o *Outbox) insert(raw []byte) error {
	var event AccountingEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	if event.ExecutionID == "" || len(event.ExecutionID) > 512 || len(raw) > MaxAccountingEventBytes || event.SchemaVersion != AccountingEventSchemaVersion {
		return errors.New("invalid accounting event")
	}
	return o.update(func(tx *bolt.Tx) error {
		events := tx.Bucket(eventsBucket)
		key := []byte(event.ExecutionID)
		if events.Get(key) != nil {
			return nil
		}
		if events.Sequence() >= uint64(o.cfg.MaxEvents) {
			return ErrOutboxFull
		}
		if err := o.checkDisk(tx); err != nil {
			return err
		}
		delivery, err := json.Marshal(Delivery{State: DeliveryPending, UpdatedAt: o.now()})
		if err != nil {
			return err
		}
		if err := events.Put(key, raw); err != nil {
			return err
		}
		if err := tx.Bucket(deliveriesBucket).Put(key, delivery); err != nil {
			return err
		}
		return events.SetSequence(events.Sequence() + 1)
	})
}

// Reserve space for copy-on-write pages and freelist growth. Refuse admission rather than evict pending events.
func (o *Outbox) checkDisk(tx *bolt.Tx) error {
	info, err := os.Stat(o.db.Path())
	if err != nil {
		return err
	}
	reserve := int64(2*1024*1024 + (o.cfg.MaxDiskMB*1024*1024/tx.DB().Info().PageSize)*16)
	if info.Size()+reserve >= int64(o.cfg.MaxDiskMB)*1024*1024 {
		return ErrOutboxFull
	}
	return nil
}

func (o *Outbox) Event(id string) (AccountingEvent, error) {
	var event AccountingEvent
	err := o.view(func(tx *bolt.Tx) error {
		raw := tx.Bucket(eventsBucket).Get([]byte(id))
		if raw == nil {
			return os.ErrNotExist
		}
		return json.Unmarshal(raw, &event)
	})
	return event, err
}

func (o *Outbox) Delivery(id string) (Delivery, error) {
	var delivery Delivery
	err := o.view(func(tx *bolt.Tx) error { return readDelivery(tx, id, &delivery) })
	return delivery, err
}

func readDelivery(tx *bolt.Tx, id string, delivery *Delivery) error {
	raw := tx.Bucket(deliveriesBucket).Get([]byte(id))
	if raw == nil {
		return os.ErrNotExist
	}
	return json.Unmarshal(raw, delivery)
}

// Claim provides transactional single-worker ownership. Ambiguous claims require explicit resolution, never automatic retry.
func (o *Outbox) Claim(id string) (bool, error) {
	claimed := false
	err := o.update(func(tx *bolt.Tx) error {
		var delivery Delivery
		if err := readDelivery(tx, id, &delivery); err != nil {
			return err
		}
		if (delivery.State != DeliveryPending && delivery.State != DeliveryRetryable) || delivery.NextAttemptAt.After(o.now()) {
			return nil
		}
		if err := o.checkDisk(tx); err != nil {
			return err
		}
		delivery.State = DeliveryAmbiguous
		delivery.Attempts++
		delivery.UpdatedAt = o.now()
		raw, err := json.Marshal(delivery)
		if err != nil {
			return err
		}
		if err := tx.Bucket(deliveriesBucket).Put([]byte(id), raw); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return claimed, err
}

// Resolve records an exporter outcome. Retryable must mean the exporter knows the event was not accepted.
func (o *Outbox) Resolve(id string, attempt uint64, state DeliveryState, next time.Time) error {
	if state != DeliveryAcknowledged && state != DeliveryRetryable && state != DeliveryAmbiguous && state != DeliveryRejected {
		return errors.New("invalid delivery outcome")
	}
	return o.update(func(tx *bolt.Tx) error {
		var delivery Delivery
		if err := readDelivery(tx, id, &delivery); err != nil {
			return err
		}
		if delivery.State != DeliveryAmbiguous || delivery.Attempts != attempt {
			return errors.New("delivery is not claimed or ambiguous")
		}
		if err := o.checkDisk(tx); err != nil {
			return err
		}
		delivery.State, delivery.UpdatedAt, delivery.NextAttemptAt = state, o.now(), next
		raw, err := json.Marshal(delivery)
		if err != nil {
			return err
		}
		return tx.Bucket(deliveriesBucket).Put([]byte(id), raw)
	})
}

func (o *Outbox) Prune() error { return o.update(o.prune) }

func (o *Outbox) prune(tx *bolt.Tx) error {
	if err := o.checkDisk(tx); err != nil {
		return err
	}
	removed := 0
	cutoff := o.now().Add(-time.Duration(o.cfg.RetentionHours) * time.Hour)
	cursor := tx.Bucket(deliveriesBucket).Cursor()
	for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
		var delivery Delivery
		if err := json.Unmarshal(raw, &delivery); err != nil {
			return err
		}
		if (delivery.State == DeliveryAcknowledged || delivery.State == DeliveryRejected) && delivery.UpdatedAt.Before(cutoff) {
			if err := tx.Bucket(eventsBucket).Delete(key); err != nil {
				return err
			}
			if err := cursor.Delete(); err != nil {
				return err
			}
			events := tx.Bucket(eventsBucket)
			if err := events.SetSequence(events.Sequence() - 1); err != nil {
				return err
			}
			removed++
			if removed == 64 {
				break
			}
		}
	}
	return nil
}

func (o *Outbox) Stats() (OutboxStats, error) {
	stats := o.Counters()
	if o.db == nil {
		return stats, nil
	}
	err := o.view(func(tx *bolt.Tx) error {
		return tx.Bucket(deliveriesBucket).ForEach(func(key, raw []byte) error {
			var delivery Delivery
			if err := json.Unmarshal(raw, &delivery); err != nil {
				return err
			}
			if delivery.State == DeliveryAcknowledged || delivery.State == DeliveryRejected {
				return nil
			}
			stats.Backlog++
			var event AccountingEvent
			if err := json.Unmarshal(tx.Bucket(eventsBucket).Get(key), &event); err != nil {
				return err
			}
			age := int64(o.now().Sub(event.CompletedAt).Seconds())
			if age > stats.OldestPendingAgeSeconds {
				stats.OldestPendingAgeSeconds = age
			}
			return nil
		})
	})
	if errors.Is(err, bolt.ErrDatabaseNotOpen) {
		o.finalMu.RLock()
		stats.Backlog, stats.OldestPendingAgeSeconds = o.finalStats.Backlog, o.finalStats.OldestPendingAgeSeconds
		o.finalMu.RUnlock()
		return stats, nil
	}
	return stats, err
}

// Close stops admission and bounds the caller's wait. A blocked filesystem operation
// cannot be cancelled safely, so ownership remains held until the worker finishes.
func (o *Outbox) Close(ctx context.Context) error {
	o.mu.Lock()
	if !o.closed {
		o.closed = true
		close(o.queue)
		go func() {
			<-o.done
			stats, err := o.Stats()
			if err != nil {
				log.WithError(err).Warn("accounting final status failed")
			}
			o.finalMu.Lock()
			o.finalStats = stats
			o.finalMu.Unlock()
			if err := o.closeAvailableStore(); err != nil {
				log.WithError(err).Error("close accounting store")
			}
			close(o.closeDone)
		}()
	}
	o.mu.Unlock()
	select {
	case <-o.closeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func boundedAccountingEvent(event AccountingEvent) bool {
	size := 0
	for _, value := range []string{event.ExecutionID, event.TraceID, event.Provider, event.ExecutorType, event.RequestedAlias, event.ExecutedModel, event.ResponseModel, event.RequestedServiceTier, event.ReportedServiceTier, event.Status, event.AccountID, event.ClientKeyID, event.SessionID, event.ParentSessionID, event.NodeKind} {
		size += len(value)
		if size > MaxAccountingEventBytes/6 {
			return false
		}
	}
	if event.Tokens != nil {
		if len(event.Tokens.Evidence) > 128 {
			return false
		}
		for key := range event.Tokens.Evidence {
			size += len(key)
			if size > MaxAccountingEventBytes/6 {
				return false
			}
		}
	}
	return true
}

// UnavailableOutbox counts losses after a startup storage failure without blocking inference.
func UnavailableOutbox() *Outbox {
	o := &Outbox{unavailable: true, queue: make(chan []byte), done: make(chan struct{}), closeDone: make(chan struct{})}
	o.failures.Add(1)
	go o.run()
	return o
}

func (o *Outbox) pruneAvailableStore() error {
	if o.db == nil {
		return nil
	}
	return o.Prune()
}

func (o *Outbox) closeAvailableStore() error {
	if o.db == nil {
		return nil
	}
	return o.db.Close()
}

func (o *Outbox) update(fn func(*bolt.Tx) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("accounting transaction failed: %v", recovered)
		}
	}()
	return o.db.Update(fn)
}

func (o *Outbox) view(fn func(*bolt.Tx) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("accounting read failed: %v", recovered)
		}
	}()
	return o.db.View(fn)
}

func (o *Outbox) Counters() OutboxStats {
	return OutboxStats{PersistenceFailures: o.failures.Load(), DroppedEvents: o.dropped.Load(), QueueDepth: len(o.queue)}
}

func openAccountingDB(path string) (db *bolt.DB, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("open accounting database: %v", recovered)
		}
	}()
	return bolt.Open(path, 0600, &bolt.Options{Timeout: time.Millisecond})
}

func validateOutbox(tx *bolt.Tx) error {
	events, deliveries := tx.Bucket(eventsBucket), tx.Bucket(deliveriesBucket)
	if events == nil && deliveries == nil {
		return nil
	}
	if events == nil || deliveries == nil {
		return errors.New("incomplete accounting store")
	}
	count := uint64(0)
	if err := events.ForEach(func(key, raw []byte) error {
		var event AccountingEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return err
		}
		if event.SchemaVersion != AccountingEventSchemaVersion || event.ExecutionID != string(key) {
			return errors.New("invalid stored accounting event")
		}
		var delivery Delivery
		if err := readDelivery(tx, string(key), &delivery); err != nil {
			return err
		}
		switch delivery.State {
		case DeliveryPending, DeliveryAcknowledged, DeliveryRetryable, DeliveryAmbiguous, DeliveryRejected:
		default:
			return errors.New("invalid stored delivery state")
		}
		count++
		return nil
	}); err != nil {
		return err
	}
	if count != events.Sequence() {
		return errors.New("invalid accounting event count")
	}
	return deliveries.ForEach(func(key, raw []byte) error {
		if events.Get(key) == nil {
			return errors.New("orphan accounting delivery")
		}
		return nil
	})
}

// ReadyIDs selects bounded due deliveries without changing their state.
func (o *Outbox) ReadyIDs(limit int) ([]string, error) {
	ids := []string{}
	if o.db == nil {
		return ids, nil
	}
	err := o.view(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(deliveriesBucket).Cursor()
		for key, raw := cursor.First(); key != nil && len(ids) < limit; key, raw = cursor.Next() {
			var d Delivery
			if err := json.Unmarshal(raw, &d); err != nil {
				return err
			}
			if (d.State == DeliveryPending || d.State == DeliveryRetryable) && !d.NextAttemptAt.After(o.now()) {
				ids = append(ids, string(key))
			}
		}
		return nil
	})
	return ids, err
}
