package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/homework-G20200607010067/week1/internal/config"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
	"github.com/homework-G20200607010067/week1/internal/services"
)

// PromptHandler exposes immutable prompt version management.
type PromptHandler struct {
	service *services.PromptService
	limits  config.LimitsConfig
}

func NewPromptHandler(service *services.PromptService, limits config.LimitsConfig) *PromptHandler {
	return &PromptHandler{service: service, limits: limits}
}

func (h *PromptHandler) Create(ctx context.Context, request *app.RequestContext) {
	var input PromptCreateRequest
	if err := decodeStrictJSON(request.Request.Body(), &input); err != nil {
		WriteError(request, core.WrapError(http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid_json", "request body must be valid JSON", err))
		return
	}
	if !safeIdentifier.MatchString(input.ID) || len(input.Name) > 200 || len(input.Description) > h.limits.MaxTemplateBytes {
		WriteError(request, core.NewError(http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid_prompt", "prompt fields are invalid"))
		return
	}
	activate := true
	if input.Activate != nil {
		activate = *input.Activate
	}
	created, err := h.service.Create(ctx, domain.PromptCreate{
		ID: input.ID, Name: strings.TrimSpace(input.Name), Description: input.Description,
		Role: domain.Role(input.Role), Content: input.Content, Activate: activate,
	})
	if err != nil {
		WriteError(request, err)
		return
	}
	request.JSON(http.StatusCreated, created)
}

func (h *PromptHandler) List(ctx context.Context, request *app.RequestContext) {
	limit, err := queryInt(request, "limit", 100, h.limits.MaxQueryLimit)
	if err != nil {
		WriteError(request, err)
		return
	}
	id := string(request.Query("prompt_id"))
	if id != "" && !safeIdentifier.MatchString(id) {
		WriteError(request, core.NewError(http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid_prompt_id", "prompt id is invalid"))
		return
	}
	items, err := h.service.List(ctx, id, limit)
	if err != nil {
		WriteError(request, err)
		return
	}
	request.JSON(http.StatusOK, PromptList{Data: items})
}

func (h *PromptHandler) Get(ctx context.Context, request *app.RequestContext) {
	id := request.Param("prompt_id")
	if !safeIdentifier.MatchString(id) {
		WriteError(request, core.NewError(http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid_prompt_id", "prompt id is invalid"))
		return
	}
	version, err := optionalQueryInt(request, "version", h.limits.MaxQueryLimit)
	if err != nil {
		WriteError(request, err)
		return
	}
	item, err := h.service.Get(ctx, id, version)
	if err != nil {
		WriteError(request, err)
		return
	}
	request.JSON(http.StatusOK, item)
}

func (h *PromptHandler) Render(ctx context.Context, request *app.RequestContext) {
	if !safeIdentifier.MatchString(request.Param("prompt_id")) {
		WriteError(request, core.NewError(http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid_prompt_id", "prompt id is invalid"))
		return
	}
	var input PromptRenderRequest
	if err := decodeStrictJSON(request.Request.Body(), &input); err != nil {
		WriteError(request, core.WrapError(http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid_json", "request body must be valid JSON", err))
		return
	}
	if input.Version != nil && *input.Version < 1 {
		WriteError(request, core.NewError(http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid_prompt_version", "prompt version must be positive"))
		return
	}
	result, err := h.service.Render(ctx, request.Param("prompt_id"), input.Version, input.Variables)
	if err != nil {
		WriteError(request, err)
		return
	}
	request.JSON(http.StatusOK, result)
}

func queryInt(request *app.RequestContext, name string, defaultValue, maximum int) (int, error) {
	raw := string(request.Query(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maximum {
		return 0, core.NewError(http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid_"+name, name+" is outside the allowed range")
	}
	return value, nil
}

func optionalQueryInt(request *app.RequestContext, name string, maximum int) (*int, error) {
	raw := string(request.Query(name))
	if raw == "" {
		return nil, nil
	}
	value, err := queryInt(request, name, 0, maximum)
	if err != nil {
		return nil, err
	}
	return &value, nil
}
