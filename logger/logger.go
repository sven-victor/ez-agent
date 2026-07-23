// Package logger defines the optional diagnostic Logger for Agent / Loop.
package logger

import "log/slog"

// Logger receives framework diagnostics at key agent/loop nodes.
// Key-value args follow slog conventions: alternating string keys and values.
// Implementations must be safe for concurrent use.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// Nop returns a Logger that discards all records.
func Nop() Logger { return nop{} }

type nop struct{}

func (nop) Debug(string, ...any) {}
func (nop) Info(string, ...any)  {}
func (nop) Warn(string, ...any)  {}
func (nop) Error(string, ...any) {}

// OrNop returns l, or Nop() when l is nil.
func OrNop(l Logger) Logger {
	if l == nil {
		return Nop()
	}
	return l
}

// FromSlog adapts a *slog.Logger. A nil input yields Nop().
func FromSlog(l *slog.Logger) Logger {
	if l == nil {
		return Nop()
	}
	return slogAdapter{l: l}
}

type slogAdapter struct {
	l *slog.Logger
}

func (s slogAdapter) Debug(msg string, args ...any) { s.l.Debug(msg, args...) }
func (s slogAdapter) Info(msg string, args ...any)  { s.l.Info(msg, args...) }
func (s slogAdapter) Warn(msg string, args ...any)  { s.l.Warn(msg, args...) }
func (s slogAdapter) Error(msg string, args ...any) { s.l.Error(msg, args...) }

// Func is a function-backed Logger. Nil level funcs are no-ops.
type Func struct {
	DebugFunc func(msg string, args ...any)
	InfoFunc  func(msg string, args ...any)
	WarnFunc  func(msg string, args ...any)
	ErrorFunc func(msg string, args ...any)
}

func (f Func) Debug(msg string, args ...any) {
	if f.DebugFunc != nil {
		f.DebugFunc(msg, args...)
	}
}
func (f Func) Info(msg string, args ...any) {
	if f.InfoFunc != nil {
		f.InfoFunc(msg, args...)
	}
}
func (f Func) Warn(msg string, args ...any) {
	if f.WarnFunc != nil {
		f.WarnFunc(msg, args...)
	}
}
func (f Func) Error(msg string, args ...any) {
	if f.ErrorFunc != nil {
		f.ErrorFunc(msg, args...)
	}
}
