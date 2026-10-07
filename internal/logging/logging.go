// Package logging provides a small leveled logger whose threshold can be
// changed at runtime. A Bound logger pins a single level for the lifetime of
// one request, so a configuration published while the request is in flight
// never changes which messages that request emits.
package logging

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Level identifies a logging threshold. The textual values are part of the
// runtime configuration API.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// ParseLevel accepts the canonical level names, case insensitive.
func ParseLevel(value string) (Level, error) {
	switch Level(strings.ToLower(strings.TrimSpace(value))) {
	case LevelDebug:
		return LevelDebug, nil
	case LevelInfo:
		return LevelInfo, nil
	case LevelWarn:
		return LevelWarn, nil
	case LevelError:
		return LevelError, nil
	default:
		return "", fmt.Errorf("log level must be one of debug, info, warn, error")
	}
}

func (l Level) severity() int32 {
	switch l {
	case LevelDebug:
		return 0
	case LevelInfo:
		return 1
	case LevelWarn:
		return 2
	case LevelError:
		return 3
	default:
		return 1
	}
}

func levelForSeverity(severity int32) Level {
	switch severity {
	case 0:
		return LevelDebug
	case 2:
		return LevelWarn
	case 3:
		return LevelError
	default:
		return LevelInfo
	}
}

// Logger serializes writes to a single destination and holds an atomically
// replaceable threshold. Derived Bound loggers share its writer and mutex.
type Logger struct {
	mu    sync.Mutex
	out   io.Writer
	level atomic.Int32
}

// New creates a logger writing to out at the given threshold.
func New(out io.Writer, level Level) *Logger {
	logger := &Logger{out: out}
	logger.level.Store(level.severity())
	return logger
}

// Discard returns a logger that drops every message. It is used by bounded
// workflow checks and tests.
func Discard() *Logger {
	return New(io.Discard, LevelInfo)
}

// SetLevel changes the threshold for every subsequently bound logger without
// touching loggers already bound to a request.
func (l *Logger) SetLevel(level Level) {
	l.level.Store(level.severity())
}

// Level reports the currently configured threshold.
func (l *Logger) Level() Level {
	return levelForSeverity(l.level.Load())
}

// At binds a logger to the supplied level for its whole lifetime.
func (l *Logger) At(level Level) Bound {
	return Bound{owner: l, level: level}
}

// Current binds a logger at the active threshold.
func (l *Logger) Current() Bound {
	return l.At(l.Level())
}

func (l *Logger) Debugf(format string, args ...any) { l.write(l.Level(), LevelDebug, format, args...) }
func (l *Logger) Infof(format string, args ...any)  { l.write(l.Level(), LevelInfo, format, args...) }
func (l *Logger) Warnf(format string, args ...any)  { l.write(l.Level(), LevelWarn, format, args...) }
func (l *Logger) Errorf(format string, args ...any) { l.write(l.Level(), LevelError, format, args...) }

func (l *Logger) write(pinned, severity Level, format string, args ...any) {
	if l == nil || l.out == nil || severity.severity() < pinned.severity() {
		return
	}
	message := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("%s level=%s msg=%s\n",
		time.Now().UTC().Format(time.RFC3339), severity, message)
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.out, line)
}

// Bound is a logger pinned to one level. The zero value discards messages.
type Bound struct {
	owner *Logger
	level Level
}

// Enabled reports whether a message at severity passes the pinned threshold.
func (b Bound) Enabled(severity Level) bool {
	return b.owner != nil && severity.severity() >= b.level.severity()
}

func (b Bound) Debugf(format string, args ...any) { b.write(LevelDebug, format, args...) }
func (b Bound) Infof(format string, args ...any)  { b.write(LevelInfo, format, args...) }
func (b Bound) Warnf(format string, args ...any)  { b.write(LevelWarn, format, args...) }
func (b Bound) Errorf(format string, args ...any) { b.write(LevelError, format, args...) }

func (b Bound) write(severity Level, format string, args ...any) {
	if b.owner == nil {
		return
	}
	b.owner.write(b.level, severity, format, args...)
}

type boundContextKey struct{}

// ContextWith attaches a bound logger to a request context.
func ContextWith(ctx context.Context, bound Bound) context.Context {
	return context.WithValue(ctx, boundContextKey{}, bound)
}

// FromContext returns the request-bound logger, or fallback when none is
// attached.
func FromContext(ctx context.Context, fallback Bound) Bound {
	if bound, ok := ctx.Value(boundContextKey{}).(Bound); ok {
		return bound
	}
	return fallback
}
