package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	log "github.com/sirupsen/logrus"
)

type jsEngine struct {
	vm            *goja.Runtime
	consoleLogger jsConsoleLogger
}

const maxJSScriptBytes = 8 * 1024 * 1024

type jsConsoleLogger func(message string) error

func newJSEngine(loggers ...jsConsoleLogger) *jsEngine {
	consoleLogger := defaultJSConsoleLogger
	if len(loggers) > 0 && loggers[0] != nil {
		consoleLogger = loggers[0]
	}
	engine := &jsEngine{
		vm:            goja.New(),
		consoleLogger: consoleLogger,
	}
	engine.initConsole()
	return engine
}

func defaultJSConsoleLogger(message string) error {
	log.Info("JS console log: ", message)
	return nil
}

func (engine *jsEngine) initConsole() {
	console := engine.vm.NewObject()
	consoleLogWrapper := func(call goja.FunctionCall) goja.Value {
		args := make([]string, len(call.Arguments))
		for i, arg := range call.Arguments {
			args[i] = fmt.Sprint(arg.Export())
		}
		message := strings.Join(args, " ")
		if errLog := engine.consoleLogger(message); errLog != nil {
			defaultJSConsoleLogger(message)
		}
		return goja.Undefined()
	}
	_ = console.Set("log", consoleLogWrapper)
	_ = engine.vm.Set("console", console)
}

func (engine *jsEngine) runProgram(program *goja.Program, timeout time.Duration) error {
	if program == nil {
		return errors.New("program is nil")
	}
	timer, done := engine.startInterruptTimer(timeout)
	defer engine.stopInterruptTimer(timer, done)

	_, err := engine.vm.RunProgram(program)
	if err != nil {
		return fmt.Errorf("failed to run JS program: %w", err)
	}
	return nil
}

var ErrFunctionNotFound = errors.New("function not found")
var errJSTimeout = errors.New("javascript execution timeout")

func (engine *jsEngine) startInterruptTimer(timeout time.Duration) (*time.Timer, <-chan struct{}) {
	done := make(chan struct{})
	timer := time.AfterFunc(timeout, func() {
		defer close(done)
		engine.vm.Interrupt(errJSTimeout)
	})
	return timer, done
}

func (engine *jsEngine) stopInterruptTimer(timer *time.Timer, done <-chan struct{}) {
	if timer == nil {
		return
	}
	if timer.Stop() {
		return
	}
	<-done
	engine.vm.ClearInterrupt()
}

func (engine *jsEngine) frozenStringArray(values []string) (goja.Value, error) {
	items := make([]interface{}, len(values))
	for i, value := range values {
		items[i] = value
	}
	array := engine.vm.NewArray(items...)
	objectValue := engine.vm.Get("Object")
	if objectValue == nil || goja.IsUndefined(objectValue) {
		return nil, errors.New("Object constructor is unavailable")
	}
	freezeValue := objectValue.ToObject(engine.vm).Get("freeze")
	freezeFunc, ok := goja.AssertFunction(freezeValue)
	if !ok {
		return nil, errors.New("Object.freeze is unavailable")
	}
	if _, errFreeze := freezeFunc(goja.Undefined(), array); errFreeze != nil {
		return nil, errFreeze
	}
	return array, nil
}

func (engine *jsEngine) callFunction(name string, timeout time.Duration, args ...interface{}) (goja.Value, error) {
	jsVal := engine.vm.Get(name)
	if jsVal == nil || goja.IsUndefined(jsVal) {
		return nil, fmt.Errorf("%w: function '%s' does not exist", ErrFunctionNotFound, name)
	}
	jsFunc, ok := goja.AssertFunction(jsVal)
	if !ok {
		return nil, fmt.Errorf("function '%s' is invalid", name)
	}

	jsArgs := make([]goja.Value, len(args))
	for i, arg := range args {
		jsArgs[i] = engine.vm.ToValue(arg)
	}

	timer, done := engine.startInterruptTimer(timeout)
	defer engine.stopInterruptTimer(timer, done)

	result, err := jsFunc(goja.Undefined(), jsArgs...)
	if err != nil {
		return nil, err
	}

	return result, nil
}

type jsCachedProgram struct {
	program *goja.Program
	modTime time.Time
}

var (
	jsProgramsMU    sync.RWMutex
	jsProgramsCache = make(map[string]jsCachedProgram)
)

func getJSProgram(path string) (*goja.Program, error) {
	cleanPath, errClean := filepath.Abs(filepath.Clean(path))
	if errClean != nil {
		return nil, errClean
	}
	resolvedPath, errEval := filepath.EvalSymlinks(cleanPath)
	if errEval != nil {
		return nil, errEval
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxJSScriptBytes {
		return nil, fmt.Errorf("JS script %s is too large: %d bytes", resolvedPath, info.Size())
	}
	modTime := info.ModTime()

	jsProgramsMU.RLock()
	cached, exists := jsProgramsCache[resolvedPath]
	jsProgramsMU.RUnlock()
	if exists && cached.modTime.Equal(modTime) {
		return cached.program, nil
	}

	data, errRead := os.ReadFile(resolvedPath)
	if errRead != nil {
		return nil, errRead
	}

	compiled, errCompile := goja.Compile(resolvedPath, string(data), false)
	if errCompile != nil {
		return nil, fmt.Errorf("failed to compile JS script %s: %w", resolvedPath, errCompile)
	}

	jsProgramsMU.Lock()
	defer jsProgramsMU.Unlock()
	if cached, exists = jsProgramsCache[resolvedPath]; exists && cached.modTime.Equal(modTime) {
		return cached.program, nil
	}

	jsProgramsCache[resolvedPath] = jsCachedProgram{
		program: compiled,
		modTime: modTime,
	}
	return compiled, nil
}
