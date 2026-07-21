package model_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sven-victor/ez-agent/model"
)

func TestExponentialBackoff(t *testing.T) {
	p := model.ExponentialBackoff{
		MaxAttempts: 3,
		Initial:     10 * time.Millisecond,
		Max:         100 * time.Millisecond,
		Multiplier:  2,
	}
	wait, ok := p.ShouldRetry(1, model.ErrThrottled)
	if !ok || wait != 10*time.Millisecond {
		t.Fatalf("1: wait=%v ok=%v", wait, ok)
	}
	wait, ok = p.ShouldRetry(2, model.ErrThrottled)
	if !ok || wait != 20*time.Millisecond {
		t.Fatalf("2: wait=%v ok=%v", wait, ok)
	}
	_, ok = p.ShouldRetry(4, model.ErrThrottled)
	if ok {
		t.Fatal("expected no retry after max")
	}
	_, ok = p.ShouldRetry(1, errors.New("fatal"))
	if ok {
		t.Fatal("non-throttled should not retry")
	}
}
