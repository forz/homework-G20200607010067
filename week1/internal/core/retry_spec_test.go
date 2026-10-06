package core_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/homework-G20200607010067/week1/internal/core"
)

func TestRetryPolicyRunsInitialAttemptPlusAtMostThreeRetries_BitsSpecUT(t *testing.T) {
	sentinel := errors.New("temporary")
	attempts := 0
	policy := core.RetryPolicy{MaxRetries: 3}
	retries, err := policy.Do(context.Background(), func(_ int) error {
		attempts++
		return sentinel
	}, func(error) bool { return true })
	if !errors.Is(err, sentinel) {
		t.Fatalf("terminal error = %v, want sentinel", err)
	}
	if attempts != 4 || retries != 3 {
		t.Fatalf("attempts=%d retries=%d, want attempts=4 retries=3", attempts, retries)
	}
}

func TestRetryPolicyBackoffIsCancellable_BitsSpecUT(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	policy := core.RetryPolicy{MaxRetries: 3, BaseDelay: time.Second, MaxDelay: time.Second}
	retries, err := policy.Do(ctx, func(_ int) error {
		attempts++
		return errors.New("temporary")
	}, func(error) bool { return true })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if attempts != 1 || retries != 0 {
		t.Fatalf("attempts=%d retries=%d, want attempts=1 retries=0", attempts, retries)
	}
}

func TestRetryPolicyExponentialCaps(t *testing.T) {
	var caps []time.Duration
	policy := core.RetryPolicy{
		MaxRetries: 3, BaseDelay: 50 * time.Millisecond, MaxDelay: 150 * time.Millisecond,
		Jitter: func(cap time.Duration) time.Duration {
			caps = append(caps, cap)
			return 0
		},
	}
	_, _ = policy.Do(context.Background(), func(int) error { return errors.New("temporary") }, func(error) bool { return true })
	want := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 150 * time.Millisecond}
	if len(caps) != len(want) {
		t.Fatalf("backoff caps=%v, want %v", caps, want)
	}
	for i := range want {
		if caps[i] != want[i] {
			t.Fatalf("backoff caps=%v, want %v", caps, want)
		}
	}
	t.Logf("exponential backoff caps with 150ms ceiling: %v", caps)
}
