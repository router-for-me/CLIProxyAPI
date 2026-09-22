package tui

import (
	"fmt"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

// LogHook is a logrus hook that captures log entries and sends them to a channel.
type LogHook struct {
	ch        chan string
	formatter log.Formatter
	mu        sync.Mutex
	levels    []log.Level
}

// NewLogHook creates a new LogHook with a buffered channel of the given size.
func NewLogHook(bufSize int) *LogHook {
	return &LogHook{
		ch:        make(chan string, bufSize),
		formatter: &log.TextFormatter{DisableColors: true, FullTimestamp: true},
		levels:    log.AllLevels,
	}
}

// SetFormatter sets a custom formatter for the hook.
func (h *LogHook) SetFormatter(f log.Formatter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.formatter = f
}

// Levels returns the log levels this hook should fire on.
func (h *LogHook) Levels() []log.Level {
	return h.levels
}

// Fire is called by logrus when a log entry is fired.
func (h *LogHook) Fire(entry *log.Entry) error {
	h.mu.Lock()
	f := h.formatter
	h.mu.Unlock()

	var line string
	if f != nil {
		b, err := f.Format(entry)
		if err == nil {
			line = strings.TrimRight(string(b), "\n\r")
		} else {
			line = fmt.Sprintf("[%s] %s", entry.Level, entry.Message)
		}
	} else {
		line = fmt.Sprintf("[%s] %s", entry.Level, entry.Message)
	}

	// Non-blocking send
	select {
	case h.ch <- line:
	default:
		// Drop oldest if full
		select {
		case <-h.ch:
		default:
		}
		select {
		case h.ch <- line:
		default:
		}
	}
	return nil
}

// Chan returns the channel to read log lines from.
func (h *LogHook) Chan() <-chan string {
	return h.ch
}
