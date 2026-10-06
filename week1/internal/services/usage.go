package services

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

// UsageStore is the best-effort persistence boundary for terminal call evidence.
type UsageStore interface {
	Insert(context.Context, domain.UsageEvent) error
}

// ConfigureUsage enables privacy-safe terminal usage persistence.
func (s *GatewayService) ConfigureUsage(store UsageStore, logger *slog.Logger) {
	s.usageStore = store
	s.logger = logger
}

func newUsageEvent(request *domain.ModelRequest, stream bool, started time.Time) domain.UsageEvent {
	return domain.UsageEvent{
		RequestID: request.RequestID, CreatedAt: started.UTC(), ModelAlias: request.ModelAlias,
		Stream: stream, Status: domain.UsageSuccess, HTTPStatus: http.StatusOK,
	}
}

func finishUsageEvent(event *domain.UsageEvent, started time.Time, response *domain.ModelResponse, err error) {
	event.LatencyMS = float64(time.Since(started).Microseconds()) / 1000
	if response != nil {
		event.Usage = response.Usage
	}
	if err == nil {
		event.Status = domain.UsageSuccess
		event.HTTPStatus = http.StatusOK
		return
	}
	normalized := core.NormalizeError(err)
	event.Status = domain.UsageError
	if normalized.Code == "request_cancelled" {
		event.Status = domain.UsageCancelled
	}
	event.HTTPStatus = normalized.Status
	event.ErrorType = normalized.Type
	event.ErrorCode = normalized.Code
}

func (s *GatewayService) persistUsage(event domain.UsageEvent) {
	if s == nil || s.usageStore == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.usageStore.Insert(ctx, event); err != nil && s.logger != nil {
		s.logger.Error("usage persistence failed", "request_id", event.RequestID, "model", event.ModelAlias)
	}
}
