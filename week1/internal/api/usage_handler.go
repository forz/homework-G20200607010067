package api

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/homework-G20200607010067/week1/internal/config"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

type UsageQuery interface {
	List(context.Context, string, int) ([]domain.UsageEvent, error)
}

type UsageHandler struct {
	query  UsageQuery
	limits config.LimitsConfig
}

func NewUsageHandler(query UsageQuery, limits config.LimitsConfig) *UsageHandler {
	return &UsageHandler{query: query, limits: limits}
}

func (h *UsageHandler) List(ctx context.Context, request *app.RequestContext) {
	limit, err := queryInt(request, "limit", 100, h.limits.MaxQueryLimit)
	if err != nil {
		WriteError(request, err)
		return
	}
	model := string(request.Query("model"))
	if model != "" && model != config.ModelPro && model != config.ModelFlash {
		WriteError(request, core.NewError(http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid_model", "usage model filter is invalid"))
		return
	}
	items, err := h.query.List(ctx, model, limit)
	if err != nil {
		WriteError(request, err)
		return
	}
	request.JSON(http.StatusOK, map[string]any{"data": items})
}
