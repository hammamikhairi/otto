// Package logger provides a simple leveled logger for the application.
// It supports three levels: off (no output), normal (info/warn/error),
// and verbose (includes debug). The logger is safe for concurrent use.
package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"sync"
)

// Level controls the verbosity of the logger.
type Level int

const (
	// LevelOff disables all log output.
	LevelOff Level = iota
	// LevelNormal enables info, warn, and error output.
	LevelNormal
	// LevelVerbose enables all output including debug.
	LevelVerbose
)

// Logger is a leveled logger. All methods are safe for concurrent use.
type Logger struct {
	mu     sync.RWMutex
	level  Level
	debug  *log.Logger
	info   *log.Logger
	warn   *log.Logger
	errLog *log.Logger
}

// New creates a logger with the given level, writing to the given output.
// If out is nil, os.Stderr is used.
func New(level Level, out io.Writer) *Logger {
	if out == nil {
		out = os.Stderr
	}

	flags := log.Ltime

	return &Logger{
		level:  level,
		debug:  log.New(out, "[DBG] ", flags),
		info:   log.New(out, "[INF] ", flags),
		warn:   log.New(out, "[WRN] ", flags),
		errLog: log.New(out, "[ERR] ", flags),
	}
}

// SetLevel changes the log level at runtime.
func (l *Logger) SetLevel(level Level) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

// Level returns the current log level.
func (l *Logger) GetLevel() Level {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.level
}

// Debug logs a message at debug level (only visible in verbose mode).
func (l *Logger) Debug(format string, args ...any) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.level >= LevelVerbose {
		l.debug.Output(2, fmt.Sprintf(format, args...))
	}
}

// Info logs a message at info level.
func (l *Logger) Info(format string, args ...any) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.level >= LevelNormal {
		l.info.Output(2, fmt.Sprintf(format, args...))
	}
}

// Warn logs a message at warn level.
func (l *Logger) Warn(format string, args ...any) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.level >= LevelNormal {
		l.warn.Output(2, fmt.Sprintf(format, args...))
	}
}

// Error logs a message at error level.
func (l *Logger) Error(format string, args ...any) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.level >= LevelNormal {
		l.errLog.Output(2, fmt.Sprintf(format, args...))
	}
}
