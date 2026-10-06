package adapters

import (
	"context"
	"net/http"
	"testing"
)

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "cancelled request stops immediately", err: context.Canceled, want: false},
		{name: "deadline exceeded is a retryable timeout", err: context.DeadlineExceeded, want: true},
		{name: "HTTP 408 is retryable", err: &HTTPStatusError{StatusCode: http.StatusRequestTimeout}, want: true},
		{name: "HTTP 409 is retryable", err: &HTTPStatusError{StatusCode: http.StatusConflict}, want: true},
		{name: "HTTP 429 is retryable", err: &HTTPStatusError{StatusCode: http.StatusTooManyRequests}, want: true},
		{name: "HTTP 500 is retryable", err: &HTTPStatusError{StatusCode: http.StatusInternalServerError}, want: true},
		{name: "ordinary client error is not retryable", err: &HTTPStatusError{StatusCode: http.StatusBadRequest}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsRetryable(test.err); got != test.want {
				t.Fatalf("IsRetryable(%v)=%v, want %v", test.err, got, test.want)
			}
		})
	}
}

func TestNormalizeUpstreamError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "rate limit status is preserved after retry exhaustion", err: &HTTPStatusError{StatusCode: http.StatusTooManyRequests}, wantStatus: http.StatusTooManyRequests, wantCode: "upstream_rate_limited"},
		{name: "request timeout becomes gateway timeout", err: &HTTPStatusError{StatusCode: http.StatusRequestTimeout}, wantStatus: http.StatusGatewayTimeout, wantCode: "upstream_timeout"},
		{name: "server failure becomes bad gateway", err: &HTTPStatusError{StatusCode: http.StatusInternalServerError}, wantStatus: http.StatusBadGateway, wantCode: "upstream_failure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NormalizeUpstreamError(test.err)
			if got.Status != test.wantStatus || got.Code != test.wantCode {
				t.Fatalf("NormalizeUpstreamError() status=%d code=%q, want status=%d code=%q", got.Status, got.Code, test.wantStatus, test.wantCode)
			}
		})
	}
}
