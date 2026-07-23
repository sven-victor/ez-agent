package logger_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/sven-victor/ez-agent/logger"
)

func TestNopAndOrNop(t *testing.T) {
	logger.Nop().Debug("x")
	logger.Nop().Info("x")
	logger.Nop().Warn("x")
	logger.Nop().Error("x")

	if logger.OrNop(nil) == nil {
		t.Fatal("OrNop(nil) must not be nil")
	}
	l := logger.Func{InfoFunc: func(string, ...any) {}}
	if logger.OrNop(l) == nil {
		t.Fatal("OrNop(non-nil) must return logger")
	}
}

func TestFromSlog(t *testing.T) {
	var buf strings.Builder
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	l := logger.FromSlog(slog.New(h))
	l.Debug("dbg", "k", 1)
	l.Info("inf", "k", 2)
	l.Warn("wrn", "k", 3)
	l.Error("err", "k", 4)
	out := buf.String()
	for _, want := range []string{"dbg", "inf", "wrn", "err"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	if logger.FromSlog(nil) == nil {
		t.Fatal("FromSlog(nil) must not be nil")
	}
}

func TestFunc(t *testing.T) {
	var got []string
	l := logger.Func{
		DebugFunc: func(msg string, _ ...any) { got = append(got, "D:"+msg) },
		InfoFunc:  func(msg string, _ ...any) { got = append(got, "I:"+msg) },
		WarnFunc:  func(msg string, _ ...any) { got = append(got, "W:"+msg) },
		ErrorFunc: func(msg string, _ ...any) { got = append(got, "E:"+msg) },
	}
	l.Debug("a")
	l.Info("b")
	l.Warn("c")
	l.Error("d")
	want := []string{"D:a", "I:b", "W:c", "E:d"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	// nil funcs are no-ops
	logger.Func{}.Info("noop")
}
