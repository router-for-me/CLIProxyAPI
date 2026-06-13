package backup

import (
	"context"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Scheduler handles scheduled automatic backups.
type Scheduler struct {
	manager    *Manager
	schedule   string
	maxBackups int
	ticker     *time.Ticker
	stopCh     chan struct{}
	mu         sync.Mutex
	running    bool
}

// NewScheduler creates a new backup scheduler.
func NewScheduler(manager *Manager, schedule string, maxBackups int) *Scheduler {
	return &Scheduler{
		manager:    manager,
		schedule:   schedule,
		maxBackups: maxBackups,
		stopCh:     make(chan struct{}),
	}
}

// Start starts the backup scheduler.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return nil
	}

	if s.schedule == "" {
		log.Debug("backup scheduler: no schedule configured")
		return nil
	}

	// Parse cron-like schedule to duration
	// For now, support simple intervals like "@hourly", "@daily", or duration strings
	interval, err := s.parseSchedule(s.schedule)
	if err != nil {
		return err
	}

	s.ticker = time.NewTicker(interval)
	s.running = true

	go s.run(ctx)

	log.Infof("backup scheduler started with interval: %v", interval)
	return nil
}

// Stop stops the backup scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	close(s.stopCh)
	if s.ticker != nil {
		s.ticker.Stop()
	}
	s.running = false

	log.Info("backup scheduler stopped")
}

// UpdateSchedule updates the schedule and restarts if running.
func (s *Scheduler) UpdateSchedule(ctx context.Context, schedule string, maxBackups int) error {
	s.Stop()

	s.mu.Lock()
	s.schedule = schedule
	s.maxBackups = maxBackups
	s.mu.Unlock()

	if schedule != "" {
		return s.Start(ctx)
	}

	return nil
}

// run is the main scheduler loop.
func (s *Scheduler) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-s.ticker.C:
			s.executeBackup()
		}
	}
}

// executeBackup performs a scheduled backup.
func (s *Scheduler) executeBackup() {
	log.Info("executing scheduled backup...")

	info, err := s.manager.Upload()
	if err != nil {
		log.WithError(err).Error("scheduled backup failed")
		return
	}

	log.Infof("scheduled backup completed: %s (size: %d bytes)", info.Name, info.Size)

	// Cleanup old backups if max is set
	if s.maxBackups > 0 {
		if err := s.manager.CleanupOldBackups(s.maxBackups); err != nil {
			log.WithError(err).Warn("failed to cleanup old backups")
		}
	}
}

// parseSchedule converts a schedule string to a time.Duration.
// Supports: @hourly, @daily, @weekly, @monthly, or duration strings like "1h", "24h"
func (s *Scheduler) parseSchedule(schedule string) (time.Duration, error) {
	switch schedule {
	case "@hourly":
		return time.Hour, nil
	case "@daily":
		return 24 * time.Hour, nil
	case "@weekly":
		return 7 * 24 * time.Hour, nil
	case "@monthly":
		return 30 * 24 * time.Hour, nil
	default:
		// Try to parse as duration
		return time.ParseDuration(schedule)
	}
}
