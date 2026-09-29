package kitutil

import (
	"fmt"
	"os"
	"sync/atomic"
)

// Kit packages log rare data-shape anomalies through these hooks. The host
// redirects them into its logging system at startup; standalone relaykit users
// get stderr defaults.

type LogFunc func(message string)

var (
	logInfo        atomic.Pointer[LogFunc]
	logError       atomic.Pointer[LogFunc]
	logSystemError atomic.Pointer[LogFunc]
)

func SetLogging(info LogFunc, errorFn LogFunc) {
	if info != nil {
		logInfo.Store(&info)
	}
	if errorFn != nil {
		logError.Store(&errorFn)
	}
}

// SetSystemErrorLogging configures the hook for internal converter failures.
func SetSystemErrorLogging(errorFn LogFunc) {
	if errorFn != nil {
		logSystemError.Store(&errorFn)
	}
}

func LogInfo(message string) {
	if fn := logInfo.Load(); fn != nil {
		(*fn)(message)
		return
	}
	fmt.Fprintf(os.Stderr, "[relaykit] %s\n", message)
}

func LogError(message string) {
	if fn := logError.Load(); fn != nil {
		(*fn)(message)
		return
	}
	fmt.Fprintf(os.Stderr, "[relaykit] ERROR %s\n", message)
}

// LogSystemError reports an internal converter failure through its dedicated
// hook, keeping it distinct from malformed request-data diagnostics.
func LogSystemError(message string) {
	if fn := logSystemError.Load(); fn != nil {
		(*fn)(message)
		return
	}
	fmt.Fprintf(os.Stderr, "[relaykit] SYSTEM ERROR %s\n", message)
}

// Debug reports whether verbose kit diagnostics are enabled. The host sets
// this once at startup (new-api mirrors common.DebugEnabled into it).
var Debug atomic.Bool
