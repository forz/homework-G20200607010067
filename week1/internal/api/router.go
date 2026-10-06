package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/homework-G20200607010067/week1/internal/config"
	"github.com/homework-G20200607010067/week1/internal/core"
)

// Handlers contains optional transport handlers registered by the application assembler.
type Handlers struct {
	Chat         app.HandlerFunc
	Models       app.HandlerFunc
	CreatePrompt app.HandlerFunc
	ListPrompts  app.HandlerFunc
	GetPrompt    app.HandlerFunc
	RenderPrompt app.HandlerFunc
	Usage        app.HandlerFunc
}

// NewServer creates a Hertz server with public health probes and authenticated
// business/admin routes. A nil optional handler is not registered.
func NewServer(
	cfg *config.Config,
	logger *slog.Logger,
	handlers Handlers,
	ready func(context.Context) error,
) *server.Hertz {
	h := server.New(
		server.WithHostPorts(cfg.Server.Address),
		server.WithReadTimeout(cfg.Server.ReadTimeout.Duration),
		server.WithIdleTimeout(cfg.Server.IdleTimeout.Duration),
		server.WithSenseClientDisconnection(true),
		server.WithMaxRequestBodySize(int(cfg.Limits.MaxBodyBytes)),
	)
	h.Use(RequestID(), Recover(logger), AccessLog(logger))
	h.GET("/healthz", func(_ context.Context, request *app.RequestContext) {
		request.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	h.GET("/readyz", func(ctx context.Context, request *app.RequestContext) {
		if ready != nil {
			if err := ready(ctx); err != nil {
				WriteError(request, core.NewError(
					http.StatusServiceUnavailable, core.ErrorTypeInternal, "not_ready", "service is not ready",
				))
				return
			}
		}
		request.JSON(http.StatusOK, map[string]string{"status": "ready"})
	})

	authorized := h.Group("", Authenticate(cfg.GatewayAPIKey))
	if handlers.Chat != nil {
		authorized.POST("/v1/chat/completions", handlers.Chat)
	}
	if handlers.Models != nil {
		authorized.GET("/v1/models", handlers.Models)
	}
	if handlers.CreatePrompt != nil {
		authorized.POST("/v1/prompts", handlers.CreatePrompt)
	}
	if handlers.ListPrompts != nil {
		authorized.GET("/v1/prompts", handlers.ListPrompts)
	}
	if handlers.GetPrompt != nil {
		authorized.GET("/v1/prompts/:prompt_id", handlers.GetPrompt)
	}
	if handlers.RenderPrompt != nil {
		authorized.POST("/v1/prompts/:prompt_id/render", handlers.RenderPrompt)
	}
	if handlers.Usage != nil {
		authorized.GET("/admin/usage", handlers.Usage)
	}
	return h
}
