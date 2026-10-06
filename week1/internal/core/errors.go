package core

import (
	"context"
	"errors"
	"net/http"
)

const (
	ErrorTypeInvalidRequest = "invalid_request_error"
	ErrorTypeAuthentication = "authentication_error"
	ErrorTypeRateLimit      = "rate_limit_error"
	ErrorTypeUpstream       = "upstream_error"
	ErrorTypeInternal       = "internal_error"
)

// GatewayError is the stable, public-safe error used by HTTP and SSE transports.
type GatewayError struct {
	Status    int
	Type      string
	Code      string
	Message   string
	Param     string
	Retryable bool
	Cause     error
}

// Error returns only the public-safe message and never formats the wrapped cause.
func (e *GatewayError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unwrap exposes the internal cause for errors.Is/errors.As without rendering it publicly.
func (e *GatewayError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// NewError constructs a stable error from an already sanitized public message.
func NewError(status int, errorType, code, message string) *GatewayError {
	return &GatewayError{Status: status, Type: errorType, Code: code, Message: message}
}

// WrapError attaches an internal cause while preserving a safe public contract.
func WrapError(status int, errorType, code, message string, cause error) *GatewayError {
	return &GatewayError{Status: status, Type: errorType, Code: code, Message: message, Cause: cause}
}

// NormalizeError maps arbitrary internal errors to a public-safe GatewayError.
func NormalizeError(err error) *GatewayError {
	if err == nil {
		return nil
	}
	var gatewayErr *GatewayError
	if errors.As(err, &gatewayErr) {
		return gatewayErr
	}
	if errors.Is(err, context.Canceled) {
		return &GatewayError{
			Status: 499, Type: ErrorTypeUpstream, Code: "request_cancelled",
			Message: "request was cancelled", Cause: err,
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &GatewayError{
			Status: http.StatusGatewayTimeout, Type: ErrorTypeUpstream, Code: "upstream_timeout",
			Message: "upstream request timed out", Retryable: true, Cause: err,
		}
	}
	return &GatewayError{
		Status: http.StatusInternalServerError, Type: ErrorTypeInternal, Code: "internal_error",
		Message: "internal server error", Cause: err,
	}
}

// WithParam returns a shallow copy with the invalid parameter name attached.
func (e *GatewayError) WithParam(param string) *GatewayError {
	if e == nil {
		return nil
	}
	copyOf := *e
	copyOf.Param = param
	return &copyOf
}

// WithRetryable returns a shallow copy with retryability attached.
func (e *GatewayError) WithRetryable(retryable bool) *GatewayError {
	if e == nil {
		return nil
	}
	copyOf := *e
	copyOf.Retryable = retryable
	return &copyOf
}
