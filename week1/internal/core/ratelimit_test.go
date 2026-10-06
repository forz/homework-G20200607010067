package core_test

import (
	"testing"

	"github.com/homework-G20200607010067/week1/internal/core"
)

func TestModelLimiter(t *testing.T) {
	limiter := core.NewModelLimiter(map[string]core.ModelRateLimit{
		"pro":   {RatePerSecond: 0.000001, Burst: 1},
		"flash": {RatePerSecond: 0.000001, Burst: 1},
	})
	if !limiter.Has("pro") || !limiter.Has("flash") || limiter.Has("unknown") {
		t.Fatal("limiter alias membership is incorrect")
	}
	if !limiter.Allow("flash") {
		t.Fatal("flash initial token was unexpectedly unavailable")
	}
	if limiter.Allow("flash") {
		t.Fatal("flash burst limit did not reject the second request")
	}
	if !limiter.Allow("pro") {
		t.Fatal("flash exhaustion leaked into the independent pro bucket")
	}
	if limiter.Allow("unknown") {
		t.Fatal("unknown model bypassed configured model limiting")
	}
	var disabled *core.ModelLimiter
	if !disabled.Allow("anything") || disabled.Has("anything") {
		t.Fatal("nil limiter did not preserve the documented disabled behavior")
	}
}
