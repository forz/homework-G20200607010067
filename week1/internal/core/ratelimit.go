package core

import "golang.org/x/time/rate"

// ModelRateLimit configures one process-local token bucket.
type ModelRateLimit struct {
	RatePerSecond float64
	Burst         int
}

// ModelLimiter owns one independent token bucket per public model alias.
// The map is immutable after construction and rate.Limiter is concurrency-safe.
type ModelLimiter struct {
	limiters map[string]*rate.Limiter
}

// NewModelLimiter constructs independent buckets for all configured aliases.
func NewModelLimiter(configs map[string]ModelRateLimit) *ModelLimiter {
	limiters := make(map[string]*rate.Limiter, len(configs))
	for alias, cfg := range configs {
		limiters[alias] = rate.NewLimiter(rate.Limit(cfg.RatePerSecond), cfg.Burst)
	}
	return &ModelLimiter{limiters: limiters}
}

// Allow consumes one token for a client request. Internal retries must not call it.
func (m *ModelLimiter) Allow(modelAlias string) bool {
	if m == nil {
		return true
	}
	limiter, ok := m.limiters[modelAlias]
	return ok && limiter.Allow()
}

// Has reports whether an alias has its own configured bucket.
func (m *ModelLimiter) Has(modelAlias string) bool {
	if m == nil {
		return false
	}
	_, ok := m.limiters[modelAlias]
	return ok
}
