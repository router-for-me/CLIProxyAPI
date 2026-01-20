// Package wakeup provides auto wakeup (warmup) functionality for AI model providers.
// It supports scheduled tasks to proactively trigger quota reset cycles.
package wakeup

import (
	"sync"
	"time"
)

// ScheduleType defines the type of scheduling pattern.
type ScheduleType string

const (
	// ScheduleTypeDaily runs at a specific time each day.
	ScheduleTypeDaily ScheduleType = "daily"
	// ScheduleTypeWeekly runs at specific times on selected weekdays.
	ScheduleTypeWeekly ScheduleType = "weekly"
	// ScheduleTypeInterval runs at fixed intervals.
	ScheduleTypeInterval ScheduleType = "interval"
	// ScheduleTypeCron uses crontab expressions for complex scheduling.
	ScheduleTypeCron ScheduleType = "cron"
)

// WakeupStatus represents the status of a wakeup execution.
type WakeupStatus string

const (
	StatusSuccess WakeupStatus = "success"
	StatusFailed  WakeupStatus = "failed"
	StatusPending WakeupStatus = "pending"
	StatusRunning WakeupStatus = "running"
)

// Schedule defines a wakeup schedule configuration.
type Schedule struct {
	// ID is the unique identifier for this schedule.
	ID string `json:"id" yaml:"id"`
	// Name is a human-readable name for this schedule.
	Name string `json:"name" yaml:"name"`
	// Enabled indicates whether this schedule is active.
	Enabled bool `json:"enabled" yaml:"enabled"`
	// Type specifies the scheduling pattern (daily, weekly, interval, cron).
	Type ScheduleType `json:"type" yaml:"type"`
	// Time is used for daily/weekly schedules in "HH:MM" format.
	Time string `json:"time,omitempty" yaml:"time,omitempty"`
	// Weekdays specifies which days to run for weekly schedules (0=Sunday, 1=Monday, etc.).
	Weekdays []int `json:"weekdays,omitempty" yaml:"weekdays,omitempty"`
	// Interval specifies the duration between runs (e.g., "2h30m") for interval schedules.
	Interval string `json:"interval,omitempty" yaml:"interval,omitempty"`
	// Cron is the crontab expression for cron-type schedules.
	Cron string `json:"cron,omitempty" yaml:"cron,omitempty"`
	// Provider specifies the target provider (e.g., "antigravity").
	Provider string `json:"provider" yaml:"provider"`
	// Models lists the models to warm up. Empty means all available models.
	Models []string `json:"models,omitempty" yaml:"models,omitempty"`
	// Accounts specifies which accounts to use. Empty means all available accounts.
	Accounts []string `json:"accounts,omitempty" yaml:"accounts,omitempty"`
	// CreatedAt is when this schedule was created.
	CreatedAt time.Time `json:"created_at,omitempty" yaml:"-"`
	// UpdatedAt is when this schedule was last updated.
	UpdatedAt time.Time `json:"updated_at,omitempty" yaml:"-"`
	// NextRun is the next scheduled execution time.
	NextRun time.Time `json:"next_run,omitempty" yaml:"-"`
}

// WakeupRecord represents a single wakeup execution record.
type WakeupRecord struct {
	// ID is the unique identifier for this record.
	ID string `json:"id"`
	// ScheduleID is the ID of the schedule that triggered this wakeup.
	ScheduleID string `json:"schedule_id"`
	// ScheduleName is the name of the schedule for display purposes.
	ScheduleName string `json:"schedule_name"`
	// AccountID identifies which account was used.
	AccountID string `json:"account_id"`
	// AccountLabel is the display label for the account.
	AccountLabel string `json:"account_label"`
	// Model is the model that was warmed up.
	Model string `json:"model"`
	// Status indicates the result of the wakeup.
	Status WakeupStatus `json:"status"`
	// Message contains additional details or error messages.
	Message string `json:"message,omitempty"`
	// Response contains a snippet of the AI response (if any).
	Response string `json:"response,omitempty"`
	// ExecutedAt is when this wakeup was executed.
	ExecutedAt time.Time `json:"executed_at"`
	// Duration is the execution time in milliseconds.
	Duration int64 `json:"duration_ms"`
}

// History manages wakeup execution history with thread-safe operations.
type History struct {
	mu      sync.RWMutex
	records []WakeupRecord
	maxSize int
}

// WakeupConfig holds the auto-wakeup configuration.
type WakeupConfig struct {
	// Enabled globally enables or disables auto-wakeup.
	Enabled bool `json:"enabled" yaml:"enabled"`
	// Schedules is the list of wakeup schedules.
	Schedules []Schedule `json:"schedules" yaml:"schedules"`
}

// WakeupState represents the current state of the wakeup scheduler.
type WakeupState struct {
	// Enabled indicates if wakeup is globally enabled.
	Enabled bool `json:"enabled"`
	// Running indicates if the scheduler is currently running.
	Running bool `json:"running"`
	// ActiveSchedules is the count of enabled schedules.
	ActiveSchedules int `json:"active_schedules"`
	// TotalSchedules is the total count of schedules.
	TotalSchedules int `json:"total_schedules"`
	// LastExecution is the time of the last wakeup execution.
	LastExecution *time.Time `json:"last_execution,omitempty"`
	// NextExecution is the time of the next scheduled wakeup.
	NextExecution *time.Time `json:"next_execution,omitempty"`
}

// TriggerRequest is the request body for manual wakeup trigger.
type TriggerRequest struct {
	// Provider specifies which provider to wake up (e.g., "antigravity").
	Provider string `json:"provider"`
	// Models optionally specifies which models to target.
	Models []string `json:"models,omitempty"`
	// Accounts optionally specifies which accounts to use.
	Accounts []string `json:"accounts,omitempty"`
}

// TriggerResponse is the response for manual wakeup trigger.
type TriggerResponse struct {
	// Success indicates if the trigger was accepted.
	Success bool `json:"success"`
	// Message provides additional information.
	Message string `json:"message"`
	// Records contains the wakeup results if executed synchronously.
	Records []WakeupRecord `json:"records,omitempty"`
}

