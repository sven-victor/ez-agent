package main

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sven-victor/ez-agent/logger"
)

// newTUILogger writes framework logs into ch (non-blocking; drops when full).
func newTUILogger(level string, ch chan<- string) logger.Logger {
	min := parseLogLevel(level)
	if min > slog.LevelError+1 { // off
		return nil
	}
	emit := func(lv slog.Level, msg string, args ...any) {
		if lv < min {
			return
		}
		line := formatLogLine(lv, msg, args...)
		select {
		case ch <- line:
		default:
			// drop rather than block the agent loop
		}
	}
	return logger.Func{
		DebugFunc: func(msg string, args ...any) { emit(slog.LevelDebug, msg, args...) },
		InfoFunc:  func(msg string, args ...any) { emit(slog.LevelInfo, msg, args...) },
		WarnFunc:  func(msg string, args ...any) { emit(slog.LevelWarn, msg, args...) },
		ErrorFunc: func(msg string, args ...any) { emit(slog.LevelError, msg, args...) },
	}
}

func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "off", "none", "silent":
		return slog.LevelError + 2
	default:
		return slog.LevelInfo
	}
}

func formatLogLine(lv slog.Level, msg string, args ...any) string {
	var b strings.Builder
	b.WriteString(time.Now().Format("15:04:05"))
	b.WriteByte(' ')
	b.WriteString(strings.ToLower(lv.String()))
	b.WriteByte(' ')
	b.WriteString(msg)
	for i := 0; i+1 < len(args); i += 2 {
		b.WriteByte(' ')
		b.WriteString(fmt.Sprint(args[i]))
		b.WriteByte('=')
		b.WriteString(fmt.Sprint(args[i+1]))
	}
	if len(args)%2 == 1 {
		b.WriteString(" _=")
		b.WriteString(fmt.Sprint(args[len(args)-1]))
	}
	return b.String()
}
