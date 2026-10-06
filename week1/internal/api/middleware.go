package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/homework-G20200607010067/week1/internal/core"
)

const requestIDContextKey = "gateway_request_id"

// RequestID generates a gateway-owned identifier and exposes it on every response.
func RequestID() app.HandlerFunc {
	return func(ctx context.Context, request *app.RequestContext) {
		requestID := newRequestID()
		request.Set(requestIDContextKey, requestID)
		request.Response.Header.Set(RequestIDHeader, requestID)
		request.Next(ctx)
	}
}

// Authenticate enforces optional constant-time Bearer-token authentication.
func Authenticate(apiKey string) app.HandlerFunc {
	return func(ctx context.Context, request *app.RequestContext) {
		if apiKey == "" {
			request.Next(ctx)
			return
		}
		provided := string(request.Request.Header.Peek("Authorization"))
		expected := "Bearer " + apiKey
		if len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			WriteError(request, core.NewError(
				http.StatusUnauthorized, core.ErrorTypeAuthentication, "invalid_api_key", "invalid or missing bearer token",
			))
			return
		}
		request.Next(ctx)
	}
}

// Recover converts panics to a generic response without exposing panic values.
func Recover(logger *slog.Logger) app.HandlerFunc {
	return func(ctx context.Context, request *app.RequestContext) {
		defer func() {
			if recover() != nil {
				logger.Error("request panic recovered", "request_id", RequestIDFrom(request))
				if request.Response.GetHijackWriter() != nil {
					// Streaming responses cannot be replaced with a JSON error.
					request.Abort()
					return
				}
				WriteError(request, core.NewError(
					http.StatusInternalServerError, core.ErrorTypeInternal, "internal_error", "internal server error",
				))
			}
		}()
		request.Next(ctx)
	}
}

// AccessLog records only bounded metadata and never request headers or bodies.
func AccessLog(logger *slog.Logger) app.HandlerFunc {
	return func(ctx context.Context, request *app.RequestContext) {
		started := time.Now()
		request.Next(ctx)
		logger.Info("request completed",
			"request_id", RequestIDFrom(request),
			"method", string(request.Method()),
			"path", string(request.Request.URI().Path()),
			"status", request.Response.StatusCode(),
			"latency_ms", float64(time.Since(started).Microseconds())/1000,
		)
	}
}

// RequestIDFrom returns the generated identifier stored by RequestID middleware.
func RequestIDFrom(request *app.RequestContext) string {
	value, ok := request.Get(requestIDContextKey)
	if !ok {
		return ""
	}
	requestID, _ := value.(string)
	return requestID
}

// WriteError aborts the Hertz chain with the stable public error envelope.
func WriteError(request *app.RequestContext, err error) {
	gatewayErr := core.NormalizeError(err)
	request.AbortWithStatusJSON(gatewayErr.Status, ErrorEnvelope{Error: ErrorDetail{
		Type: gatewayErr.Type, Code: gatewayErr.Code, Message: gatewayErr.Message,
		RequestID: RequestIDFrom(request), Retryable: gatewayErr.Retryable, Param: gatewayErr.Param,
	}})
}

func newRequestID() string {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "req_" + hex.EncodeToString([]byte(time.Now().UTC().Format("20060102150405.000000000")))
	}
	return "req_" + hex.EncodeToString(random)
}
