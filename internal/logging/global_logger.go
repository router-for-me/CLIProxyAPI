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
// This formatter adds timestamp, level, and source location to each log entry.
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

	var formatted string
	if entry.Caller != nil {
		formatted = fmt.Sprintf("[%s] [%s] [%s:%d] %s\n", timestamp, entry.Level, filepath.Base(entry.Caller.File), entry.Caller.Line, message)
	} else {
		formatted = fmt.Sprintf("[%s] [%s] %s\n", timestamp, entry.Level, message)
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
func ConfigureLogOutput(loggingToFile bool) error {
	SetupBaseLogger()

	writerMu.Lock()
	defer writerMu.Unlock()

	if loggingToFile {
		logDir := "logs"
		if base := util.WritablePath(); base != "" {
			logDir = filepath.Join(base, "logs")
		}
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return fmt.Errorf("logging: failed to create log directory: %w", err)
		}
		if logWriter != nil {
			_ = logWriter.Close()
		}
		logWriter = &lumberjack.Logger{
			Filename:   filepath.Join(logDir, "main.log"),
			MaxSize:    10,
			MaxBackups: 0,
			MaxAge:     0,
			Compress:   false,
		}
		log.SetOutput(logWriter)
		return nil
	}

	if logWriter != nil {
		_ = logWriter.Close()
		logWriter = nil
	}
	log.SetOutput(os.Stdout)
	return nil
}

func closeLogOutputs() {
	writerMu.Lock()
	defer writerMu.Unlock()

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
