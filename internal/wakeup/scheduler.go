package wakeup

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Scheduler manages wakeup schedules and executes them on time.
type Scheduler struct {
	mu          sync.RWMutex
	cfg         *config.Config
	authManager *coreauth.Manager
	executor    *Executor
	history     *History
	cron        *cron.Cron
	schedules   map[string]*scheduleEntry
	running     bool
	stopCh      chan struct{}
}

type scheduleEntry struct {
	schedule Schedule
	cronID   cron.EntryID
	ticker   *time.Ticker
	stopCh   chan struct{}
}

// NewScheduler creates a new wakeup scheduler.
func NewScheduler(cfg *config.Config, authManager *coreauth.Manager) *Scheduler {
	history := NewHistory(defaultHistoryMaxSize)
	executor := NewExecutor(cfg, authManager, history)

	return &Scheduler{
		cfg:         cfg,
		authManager: authManager,
		executor:    executor,
		history:     history,
		cron:        cron.New(cron.WithLocation(time.Local)),
		schedules:   make(map[string]*scheduleEntry),
		stopCh:      make(chan struct{}),
	}
}

// SetConfig updates the configuration reference.
func (s *Scheduler) SetConfig(cfg *config.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	s.executor.SetConfig(cfg)
}

// SetAuthManager updates the auth manager reference.
func (s *Scheduler) SetAuthManager(manager *coreauth.Manager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authManager = manager
	s.executor.SetAuthManager(manager)
}

// Start starts the scheduler and loads schedules from config.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return nil
	}

	s.cron.Start()
	s.running = true

	// Load schedules from config
	if s.cfg != nil && s.cfg.AutoWakeup.Enabled {
		for _, cs := range s.cfg.AutoWakeup.Schedules {
			sched := convertConfigSchedule(cs)
			if err := s.addScheduleInternal(sched); err != nil {
				log.Warnf("wakeup: failed to add schedule %s: %v", sched.ID, err)
			}
		}
	}

	log.Info("wakeup: scheduler started")
	return nil
}

// Stop stops the scheduler and all scheduled tasks.
func (s *Scheduler) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	// Stop all interval-based schedules
	for _, entry := range s.schedules {
		if entry.stopCh != nil {
			close(entry.stopCh)
		}
		if entry.ticker != nil {
			entry.ticker.Stop()
		}
	}

	s.cron.Stop()
	s.running = false
	s.schedules = make(map[string]*scheduleEntry)

	log.Info("wakeup: scheduler stopped")
	return nil
}

// Reload reloads schedules from the current configuration.
func (s *Scheduler) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear existing schedules
	for id, entry := range s.schedules {
		if entry.cronID != 0 {
			s.cron.Remove(entry.cronID)
		}
		if entry.stopCh != nil {
			close(entry.stopCh)
		}
		if entry.ticker != nil {
			entry.ticker.Stop()
		}
		delete(s.schedules, id)
	}

	// Reload from config
	if s.cfg != nil && s.cfg.AutoWakeup.Enabled {
		for _, cs := range s.cfg.AutoWakeup.Schedules {
			sched := convertConfigSchedule(cs)
			if err := s.addScheduleInternal(sched); err != nil {
				log.Warnf("wakeup: failed to reload schedule %s: %v", sched.ID, err)
			}
		}
	}

	log.Info("wakeup: schedules reloaded")
	return nil
}

// GetState returns the current scheduler state.
func (s *Scheduler) GetState() WakeupState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state := WakeupState{
		Enabled:        s.cfg != nil && s.cfg.AutoWakeup.Enabled,
		Running:        s.running,
		TotalSchedules: len(s.schedules),
		LastExecution:  s.history.LastExecution(),
	}

	for _, entry := range s.schedules {
		if entry.schedule.Enabled {
			state.ActiveSchedules++
		}
		if entry.schedule.NextRun.After(time.Now()) {
			if state.NextExecution == nil || entry.schedule.NextRun.Before(*state.NextExecution) {
				t := entry.schedule.NextRun
				state.NextExecution = &t
			}
		}
	}

	return state
}

// GetSchedules returns all configured schedules.
func (s *Scheduler) GetSchedules() []Schedule {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Schedule, 0, len(s.schedules))
	for _, entry := range s.schedules {
		result = append(result, entry.schedule)
	}
	return result
}

// GetSchedule returns a specific schedule by ID.
func (s *Scheduler) GetSchedule(id string) (Schedule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if entry, ok := s.schedules[id]; ok {
		return entry.schedule, true
	}
	return Schedule{}, false
}

// AddSchedule adds a new schedule.
func (s *Scheduler) AddSchedule(sched Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sched.ID == "" {
		sched.ID = uuid.New().String()
	}
	if _, exists := s.schedules[sched.ID]; exists {
		return fmt.Errorf("schedule with ID %s already exists", sched.ID)
	}

	sched.CreatedAt = time.Now()
	sched.UpdatedAt = sched.CreatedAt

	return s.addScheduleInternal(sched)
}

// UpdateSchedule updates an existing schedule.
func (s *Scheduler) UpdateSchedule(sched Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.schedules[sched.ID]
	if !ok {
		return fmt.Errorf("schedule with ID %s not found", sched.ID)
	}

	// Remove old schedule
	if existing.cronID != 0 {
		s.cron.Remove(existing.cronID)
	}
	if existing.stopCh != nil {
		close(existing.stopCh)
	}
	if existing.ticker != nil {
		existing.ticker.Stop()
	}

	sched.CreatedAt = existing.schedule.CreatedAt
	sched.UpdatedAt = time.Now()

	delete(s.schedules, sched.ID)
	return s.addScheduleInternal(sched)
}

// DeleteSchedule removes a schedule.
func (s *Scheduler) DeleteSchedule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.schedules[id]
	if !ok {
		return fmt.Errorf("schedule with ID %s not found", id)
	}

	if entry.cronID != 0 {
		s.cron.Remove(entry.cronID)
	}
	if entry.stopCh != nil {
		close(entry.stopCh)
	}
	if entry.ticker != nil {
		entry.ticker.Stop()
	}

	delete(s.schedules, id)
	return nil
}

// Trigger manually triggers a wakeup execution.
func (s *Scheduler) Trigger(ctx context.Context, req TriggerRequest) TriggerResponse {
	records := s.executor.Execute(ctx, "manual", "Manual Trigger", req.Provider, req.Models, req.Accounts)
	return TriggerResponse{
		Success: true,
		Message: fmt.Sprintf("Triggered wakeup for %d accounts", len(records)),
		Records: records,
	}
}

// GetHistory returns the wakeup history.
func (s *Scheduler) GetHistory() *History {
	return s.history
}

// convertConfigSchedule converts a config.WakeupSchedule to a wakeup.Schedule.
func convertConfigSchedule(cs config.WakeupSchedule) Schedule {
	return Schedule{
		ID:       cs.ID,
		Name:     cs.Name,
		Enabled:  cs.Enabled,
		Type:     ScheduleType(cs.Type),
		Time:     cs.Time,
		Weekdays: cs.Weekdays,
		Interval: cs.Interval,
		Cron:     cs.Cron,
		Provider: cs.Provider,
		Models:   cs.Models,
		Accounts: cs.Accounts,
	}
}

func (s *Scheduler) addScheduleInternal(sched Schedule) error {
	if !sched.Enabled {
		s.schedules[sched.ID] = &scheduleEntry{schedule: sched}
		return nil
	}

	entry := &scheduleEntry{schedule: sched}

	switch sched.Type {
	case ScheduleTypeCron:
		cronExpr := sched.Cron
		if cronExpr == "" {
			return fmt.Errorf("cron expression is required for cron schedule type")
		}
		cronID, err := s.cron.AddFunc(cronExpr, func() {
			s.executeSchedule(sched)
		})
		if err != nil {
			return fmt.Errorf("invalid cron expression: %w", err)
		}
		entry.cronID = cronID
		// Calculate next run
		cronEntry := s.cron.Entry(cronID)
		if !cronEntry.Next.IsZero() {
			entry.schedule.NextRun = cronEntry.Next
		}

	case ScheduleTypeDaily:
		cronExpr, err := dailyToCron(sched.Time)
		if err != nil {
			return err
		}
		cronID, err := s.cron.AddFunc(cronExpr, func() {
			s.executeSchedule(sched)
		})
		if err != nil {
			return fmt.Errorf("failed to add daily schedule: %w", err)
		}
		entry.cronID = cronID
		cronEntry := s.cron.Entry(cronID)
		if !cronEntry.Next.IsZero() {
			entry.schedule.NextRun = cronEntry.Next
		}

	case ScheduleTypeWeekly:
		cronExpr, err := weeklyToCron(sched.Time, sched.Weekdays)
		if err != nil {
			return err
		}
		cronID, err := s.cron.AddFunc(cronExpr, func() {
			s.executeSchedule(sched)
		})
		if err != nil {
			return fmt.Errorf("failed to add weekly schedule: %w", err)
		}
		entry.cronID = cronID
		cronEntry := s.cron.Entry(cronID)
		if !cronEntry.Next.IsZero() {
			entry.schedule.NextRun = cronEntry.Next
		}

	case ScheduleTypeInterval:
		duration, err := time.ParseDuration(sched.Interval)
		if err != nil {
			return fmt.Errorf("invalid interval duration: %w", err)
		}
		if duration < time.Minute {
			return fmt.Errorf("interval must be at least 1 minute")
		}
		entry.ticker = time.NewTicker(duration)
		entry.stopCh = make(chan struct{})
		entry.schedule.NextRun = time.Now().Add(duration)
		go s.runIntervalSchedule(sched, entry.ticker, entry.stopCh)

	default:
		return fmt.Errorf("unknown schedule type: %s", sched.Type)
	}

	s.schedules[sched.ID] = entry
	log.Infof("wakeup: added schedule %s (%s)", sched.Name, sched.ID)
	return nil
}

func (s *Scheduler) executeSchedule(sched Schedule) {
	ctx := context.Background()
	s.executor.Execute(ctx, sched.ID, sched.Name, sched.Provider, sched.Models, sched.Accounts)
}

func (s *Scheduler) runIntervalSchedule(sched Schedule, ticker *time.Ticker, stopCh chan struct{}) {
	for {
		select {
		case <-ticker.C:
			s.executeSchedule(sched)
		case <-stopCh:
			return
		}
	}
}

func dailyToCron(timeStr string) (string, error) {
	parts := strings.Split(timeStr, ":")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid time format, expected HH:MM")
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return "", fmt.Errorf("invalid hour")
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return "", fmt.Errorf("invalid minute")
	}
	return fmt.Sprintf("%d %d * * *", minute, hour), nil
}

func weeklyToCron(timeStr string, weekdays []int) (string, error) {
	parts := strings.Split(timeStr, ":")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid time format, expected HH:MM")
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return "", fmt.Errorf("invalid hour")
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return "", fmt.Errorf("invalid minute")
	}
	if len(weekdays) == 0 {
		weekdays = []int{1, 2, 3, 4, 5} // Default to weekdays
	}
	days := make([]string, len(weekdays))
	for i, d := range weekdays {
		days[i] = strconv.Itoa(d)
	}
	return fmt.Sprintf("%d %d * * %s", minute, hour, strings.Join(days, ",")), nil
}

