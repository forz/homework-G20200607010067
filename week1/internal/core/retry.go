package core

import (
	"context"
	"math/rand/v2"
	"time"
)

// RetryPolicy owns the gateway-level retry budget and cancellable backoff.
type RetryPolicy struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	Jitter     func(time.Duration) time.Duration
}

// Do executes an initial attempt plus at most MaxRetries retry attempts.
// It returns the number of retry attempts that were actually started.
func (p RetryPolicy) Do(
	ctx context.Context,
	operation func(attempt int) error,
	shouldRetry func(error) bool,
) (int, error) {
	for attempt := 0; ; attempt++ {
		err := operation(attempt)
		if err == nil {
			return attempt, nil
		}
		if attempt >= p.MaxRetries || !shouldRetry(err) {
			return attempt, err
		}
		if waitErr := p.Wait(ctx, attempt); waitErr != nil {
			return attempt, waitErr
		}
	}
}

// Wait performs one context-aware full-jitter exponential backoff interval.
func (p RetryPolicy) Wait(ctx context.Context, retryIndex int) error {
	delay := p.delay(retryIndex)
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p RetryPolicy) delay(retryIndex int) time.Duration {
	capDelay := p.BaseDelay
	for i := 0; i < retryIndex && capDelay < p.MaxDelay; i++ {
		if capDelay > p.MaxDelay/2 {
			capDelay = p.MaxDelay
			break
		}
		capDelay *= 2
	}
	if p.MaxDelay > 0 && capDelay > p.MaxDelay {
		capDelay = p.MaxDelay
	}
	if capDelay <= 0 {
		return 0
	}
	if p.Jitter != nil {
		return p.Jitter(capDelay)
	}
	return time.Duration(rand.Int64N(int64(capDelay) + 1))
}
