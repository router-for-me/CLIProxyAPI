package logging

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	setupOnce      sync.Once
	writerMu       sync.Mutex
	logWriter      *lumberjack.Logger
	ginInfoWriter  *io.PipeWriter
	ginErrorWriter *io.PipeWriter
)

// LogFormatter defines a custom log format for logrus.
// This formatter adds timestamp, level, request ID, and source location to each log entry.
// Format: [2025-12-23 20:14:04] [debug] [manager.go:524] | a1b2c3d4 | Use API key sk-9...0RHO for model gpt-5.2
type LogFormatter struct{}

// Format renders a single log entry with custom formatting.
func (m *LogFormatter) Format(entry *log.Entry) ([]byte, error) {
	var buffer *bytes.Buffer
	if entry.Buffer != nil {
		buffer = entry.Buffer
	} else {
		buffer = &bytes.Buffer{}
	}

	timestamp := entry.Time.Format("2006-01-02 15:04:05")
	message := strings.TrimRight(entry.Message, "\r\n")

	reqID := ""
	if id, ok := entry.Data["request_id"].(string); ok && id != "" {
		reqID = id
	}

	levelStr := fmt.Sprintf("%-5s", entry.Level.String())

	var formatted string
	if reqID != "" && entry.Caller != nil {
		formatted = fmt.Sprintf("[%s] [%s] [%s:%d] | %s | %s\n", timestamp, levelStr, filepath.Base(entry.Caller.File), entry.Caller.Line, reqID, message)
	} else if reqID != "" {
		formatted = fmt.Sprintf("[%s] [%s] | %s | %s\n", timestamp, levelStr, reqID, message)
	} else if entry.Caller != nil {
		formatted = fmt.Sprintf("[%s] [%s] [%s:%d] %s\n", timestamp, levelStr, filepath.Base(entry.Caller.File), entry.Caller.Line, message)
	} else {
		formatted = fmt.Sprintf("[%s] [%s] %s\n", timestamp, levelStr, message)
	}
	buffer.WriteString(formatted)

	return buffer.Bytes(), nil
}

// SetupBaseLogger configures the shared logrus instance and Gin writers.
// It is safe to call multiple times; initialization happens only once.
func SetupBaseLogger() {
	setupOnce.Do(func() {
		log.SetOutput(os.Stdout)
		log.SetReportCaller(true)
		log.SetFormatter(&LogFormatter{})

		ginInfoWriter = log.StandardLogger().Writer()
		gin.DefaultWriter = ginInfoWriter
		ginErrorWriter = log.StandardLogger().WriterLevel(log.ErrorLevel)
		gin.DefaultErrorWriter = ginErrorWriter
		gin.DebugPrintFunc = func(format string, values ...interface{}) {
			format = strings.TrimRight(format, "\r\n")
			log.StandardLogger().Infof(format, values...)
		}

		log.RegisterExitHandler(closeLogOutputs)
	})
}

// ConfigureLogOutput switches the global log destination between rotating files and stdout.
// When logsMaxTotalSizeMB > 0, a background cleaner removes the oldest log files in the logs directory
// until the total size is within the limit.
func ConfigureLogOutput(loggingToFile bool, logsMaxTotalSizeMB int) error {
	SetupBaseLogger()

	writerMu.Lock()
	defer writerMu.Unlock()

	logDir := "logs"
	if base := util.WritablePath(); base != "" {
		logDir = filepath.Join(base, "logs")
	}

	protectedPath := ""
	if loggingToFile {
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return fmt.Errorf("logging: failed to create log directory: %w", err)
		}
		if logWriter != nil {
			_ = logWriter.Close()
		}
		protectedPath = filepath.Join(logDir, "main.log")
		logWriter = &lumberjack.Logger{
			Filename:   protectedPath,
			MaxSize:    10,
			MaxBackups: 0,
			MaxAge:     0,
			Compress:   false,
		}
		log.SetOutput(logWriter)
	} else {
		if logWriter != nil {
			_ = logWriter.Close()
			logWriter = nil
		}
		log.SetOutput(os.Stdout)
	}

	configureLogDirCleanerLocked(logDir, logsMaxTotalSizeMB, protectedPath)
	return nil
}

func closeLogOutputs() {
	writerMu.Lock()
	defer writerMu.Unlock()

	stopLogDirCleanerLocked()

	if logWriter != nil {
		_ = logWriter.Close()
		logWriter = nil
	}
	if ginInfoWriter != nil {
		_ = ginInfoWriter.Close()
		ginInfoWriter = nil
	}
	if ginErrorWriter != nil {
		_ = ginErrorWriter.Close()
		ginErrorWriter = nil
	}
}
