package model

import (
	"errors"
	"math"
	"time"
)

// ErrThrottled indicates a transient rate-limit / overload from the provider.
var ErrThrottled = errors.New("model throttled")

// RetryPolicy decides whether to retry a failed model call and how long to wait.
type RetryPolicy interface {
	// ShouldRetry is called after a failed Stream (excluding context overflow).
	// attempt is 1-based for the upcoming retry (1 = first retry after initial failure).
	ShouldRetry(attempt int, err error) (wait time.Duration, retry bool)
}

// ExponentialBackoff retries transient errors with exponential delay.
type ExponentialBackoff struct {
	MaxAttempts int           // default 3 (retries after the first failure)
	Initial     time.Duration // default 200ms
	Max         time.Duration // default 5s
	Multiplier  float64       // default 2
	// Retryable reports whether err is retryable. nil → ErrThrottled or errors.Is(err, ErrThrottled).
	Retryable func(error) bool
}

func (p ExponentialBackoff) ShouldRetry(attempt int, err error) (time.Duration, bool) {
	max := p.MaxAttempts
	if max <= 0 {
		max = 3
	}
	if attempt > max {
		return 0, false
	}
	retryable := p.Retryable
	if retryable == nil {
		retryable = defaultRetryable
	}
	if !retryable(err) {
		return 0, false
	}
	initial := p.Initial
	if initial <= 0 {
		initial = 200 * time.Millisecond
	}
	maxWait := p.Max
	if maxWait <= 0 {
		maxWait = 5 * time.Second
	}
	mult := p.Multiplier
	if mult < 1 {
		mult = 2
	}
	wait := time.Duration(float64(initial) * math.Pow(mult, float64(attempt-1)))
	if wait > maxWait {
		wait = maxWait
	}
	return wait, true
}

func defaultRetryable(err error) bool {
	return errors.Is(err, ErrThrottled)
}
