package services

import (
	"net/http"
	"sort"

	"github.com/homework-G20200607010067/week1/internal/config"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

// ModelRoute binds one public alias to exactly one upstream configuration and adapter.
type ModelRoute struct {
	Alias         string
	UpstreamModel string
	Adapter       domain.ModelAdapter
}

// ModelRouter is immutable after construction and safe for concurrent reads.
type ModelRouter struct {
	routes map[string]ModelRoute
	models []string
}

// NewModelRouter validates that every configured alias has exactly one adapter.
func NewModelRouter(models map[string]config.ModelConfig, adapters map[string]domain.ModelAdapter) (*ModelRouter, error) {
	routes := make(map[string]ModelRoute, len(models))
	aliases := make([]string, 0, len(models))
	for alias, modelConfig := range models {
		adapter, ok := adapters[alias]
		if !ok || adapter == nil || adapter.Protocol() != modelConfig.Protocol {
			return nil, core.NewError(
				http.StatusInternalServerError, core.ErrorTypeInternal, "invalid_model_binding", "model routing is not configured",
			)
		}
		routes[alias] = ModelRoute{Alias: alias, UpstreamModel: modelConfig.UpstreamModel, Adapter: adapter}
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	return &ModelRouter{routes: routes, models: aliases}, nil
}

// Resolve returns the immutable route for a public alias.
func (r *ModelRouter) Resolve(alias string) (ModelRoute, error) {
	if r == nil {
		return ModelRoute{}, core.NewError(
			http.StatusInternalServerError, core.ErrorTypeInternal, "router_unavailable", "model routing is unavailable",
		)
	}
	route, ok := r.routes[alias]
	if !ok {
		return ModelRoute{}, core.NewError(
			http.StatusNotFound, core.ErrorTypeInvalidRequest, "model_not_found", "requested model was not found",
		).WithParam("model")
	}
	return route, nil
}

// Models returns a stable sorted copy of all public aliases.
func (r *ModelRouter) Models() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.models...)
}
