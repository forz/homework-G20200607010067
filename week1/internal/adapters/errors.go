package adapters

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/homework-G20200607010067/week1/internal/core"
)

// IsRetryable reports whether an upstream transport failure may consume the
// gateway-owned retry budget.
func IsRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var gatewayError *core.GatewayError
	if errors.As(err, &gatewayError) {
		return gatewayError.Retryable
	}
	// A provider attempt may time out while the caller's context is still live.
	// The retry policy separately stops when the caller's context expires.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var statusError *HTTPStatusError
	if errors.As(err, &statusError) {
		return statusError.StatusCode == http.StatusRequestTimeout ||
			statusError.StatusCode == http.StatusConflict ||
			statusError.StatusCode == http.StatusTooManyRequests ||
			statusError.StatusCode >= http.StatusInternalServerError
	}
	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
}

// NormalizeUpstreamError maps SDK wrappers and transport errors to a stable,
// body-free public contract.
func NormalizeUpstreamError(err error) *core.GatewayError {
	if err == nil {
		return nil
	}
	var gatewayError *core.GatewayError
	if errors.As(err, &gatewayError) {
		return gatewayError
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return core.NormalizeError(err)
	}
	var statusError *HTTPStatusError
	if errors.As(err, &statusError) {
		retryable := IsRetryable(err)
		switch statusError.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return core.WrapError(http.StatusBadGateway, core.ErrorTypeUpstream, "upstream_authentication_failed", "upstream authentication failed", err)
		case http.StatusTooManyRequests:
			return core.WrapError(http.StatusTooManyRequests, core.ErrorTypeRateLimit, "upstream_rate_limited", "upstream rate limit was exceeded", err).WithRetryable(retryable)
		case http.StatusRequestTimeout:
			return core.WrapError(http.StatusGatewayTimeout, core.ErrorTypeUpstream, "upstream_timeout", "upstream request timed out", err).WithRetryable(retryable)
		default:
			return core.WrapError(http.StatusBadGateway, core.ErrorTypeUpstream, "upstream_failure", "upstream model request failed", err).WithRetryable(retryable)
		}
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return core.WrapError(http.StatusBadGateway, core.ErrorTypeUpstream, "upstream_connection_error", "upstream connection failed", err).WithRetryable(IsRetryable(err))
	}
	return core.WrapError(http.StatusBadGateway, core.ErrorTypeUpstream, "upstream_failure", "upstream model request failed", err)
}
