package backup

import (
	"time"

	log "github.com/sirupsen/logrus"
)

// Scheduler runs a backup job on a fixed interval. Interval <= 0 disables it.
type Scheduler struct {
	interval time.Duration
	job      func()
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// NewScheduler builds a scheduler. job is invoked on each tick; if a previous
// run is still in flight the tick is skipped (no overlapping backups).
func NewScheduler(interval time.Duration, job func()) *Scheduler {
	return &Scheduler{interval: interval, job: job, stopCh: make(chan struct{}), doneCh: make(chan struct{})}
}

// Start launches the scheduler loop in the background. No-op when interval <= 0 or job == nil.
func (s *Scheduler) Start() {
	if s.interval <= 0 || s.job == nil {
		return
	}
	go s.loop()
}

// Stop terminates the scheduler loop and waits for it to finish.
func (s *Scheduler) Stop() {
	if s.interval <= 0 || s.job == nil {
		return
	}
	close(s.stopCh)
	<-s.doneCh
}

func (s *Scheduler) loop() {
	defer close(s.doneCh)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.WithField("panic", r).Error("backup scheduler: job panicked")
					}
				}()
				s.job()
			}()
		}
	}
}
