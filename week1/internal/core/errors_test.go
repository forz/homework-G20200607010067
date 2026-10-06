package core_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/homework-G20200607010067/week1/internal/core"
)

func TestNormalizeError(t *testing.T) {
	t.Run("preserves gateway errors", func(t *testing.T) {
		cause := errors.New("private")
		original := core.WrapError(http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid", "safe", cause).
			WithParam("model").WithRetryable(true)
		got := core.NormalizeError(original)
		if got != original || got.Param != "model" || !got.Retryable || !errors.Is(got, cause) {
			t.Fatalf("normalized gateway error=%#v", got)
		}
		if got.Error() != "safe" {
			t.Fatalf("public error leaked or changed the safe message: %q", got.Error())
		}
	})

	t.Run("maps cancellation and timeout", func(t *testing.T) {
		cancelled := core.NormalizeError(context.Canceled)
		if cancelled.Status != 499 || cancelled.Code != "request_cancelled" {
			t.Fatalf("cancelled mapping=%#v", cancelled)
		}
		timedOut := core.NormalizeError(context.DeadlineExceeded)
		if timedOut.Status != http.StatusGatewayTimeout || timedOut.Code != "upstream_timeout" || !timedOut.Retryable {
			t.Fatalf("timeout mapping=%#v", timedOut)
		}
	})

	t.Run("hides arbitrary internal errors", func(t *testing.T) {
		got := core.NormalizeError(errors.New("api-key-secret"))
		if got.Status != http.StatusInternalServerError || got.Code != "internal_error" || got.Error() != "internal server error" {
			t.Fatalf("internal mapping=%#v", got)
		}
	})
}
