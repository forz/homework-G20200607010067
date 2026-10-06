package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/homework-G20200607010067/week1/internal/adapters"
	"github.com/homework-G20200607010067/week1/internal/adapters/anthropic"
	adapteropenai "github.com/homework-G20200607010067/week1/internal/adapters/openai"
	"github.com/homework-G20200607010067/week1/internal/api"
	"github.com/homework-G20200607010067/week1/internal/config"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
	gatewaysqlite "github.com/homework-G20200607010067/week1/internal/persistence/sqlite"
	"github.com/homework-G20200607010067/week1/internal/services"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load("")
	if err != nil {
		fatal(logger, "load configuration", err)
	}
	ctx := context.Background()
	database, err := gatewaysqlite.Open(ctx, cfg.Database.Path, cfg.Database.BusyTimeout.Duration)
	if err != nil {
		fatal(logger, "open database", err)
	}
	defer database.Close()
	maximumTimeout := cfg.Models[config.ModelPro].Timeout.Duration
	if flashTimeout := cfg.Models[config.ModelFlash].Timeout.Duration; flashTimeout > maximumTimeout {
		maximumTimeout = flashTimeout
	}
	httpClient := adapters.NewHTTPClient(maximumTimeout)
	defer adapters.CloseIdleConnections(httpClient)
	openAIAdapter, err := adapteropenai.New(ctx, cfg.Models[config.ModelPro], httpClient)
	if err != nil {
		fatal(logger, "create OpenAI Responses adapter", err)
	}
	anthropicAdapter, err := anthropic.New(ctx, cfg.Models[config.ModelFlash], httpClient)
	if err != nil {
		fatal(logger, "create Anthropic Messages adapter", err)
	}
	modelRouter, err := services.NewModelRouter(cfg.Models, map[string]domain.ModelAdapter{
		config.ModelPro: openAIAdapter, config.ModelFlash: anthropicAdapter,
	})
	if err != nil {
		fatal(logger, "create model router", err)
	}
	promptRepository := gatewaysqlite.NewPromptRepository(database, cfg.Limits.MaxQueryLimit)
	promptService := services.NewPromptService(
		promptRepository, cfg.Limits.MaxTemplateBytes, cfg.Limits.MaxRenderedBytes,
	)
	usageRepository := gatewaysqlite.NewUsageRepository(database, cfg.Limits.MaxQueryLimit)
	gatewayService := services.NewGatewayService(modelRouter)
	gatewayService.ConfigureLimits(cfg.Limits)
	gatewayService.ConfigurePrompts(promptService)
	gatewayService.ConfigureUsage(usageRepository, logger)
	rateLimits := make(map[string]core.ModelRateLimit, len(cfg.Models))
	for alias, modelConfig := range cfg.Models {
		rateLimits[alias] = core.ModelRateLimit{
			RatePerSecond: modelConfig.RatePerSecond, Burst: modelConfig.Burst,
		}
	}
	gatewayService.ConfigureResilience(core.RetryPolicy{
		MaxRetries: cfg.Retry.MaxRetries,
		BaseDelay:  cfg.Retry.BaseDelay.Duration, MaxDelay: cfg.Retry.MaxDelay.Duration,
	}, core.NewModelLimiter(rateLimits))
	chatHandler := api.NewHandler(gatewayService, cfg.Limits)
	promptHandler := api.NewPromptHandler(promptService, cfg.Limits)
	usageHandler := api.NewUsageHandler(usageRepository, cfg.Limits)
	server := api.NewServer(cfg, logger, api.Handlers{
		Chat: chatHandler.ChatCompletions, Models: chatHandler.Models,
		CreatePrompt: promptHandler.Create, ListPrompts: promptHandler.List,
		GetPrompt: promptHandler.Get, RenderPrompt: promptHandler.Render,
		Usage: usageHandler.List,
	}, database.PingContext)
	logger.Info("gateway listening", "address", cfg.Server.Address)
	server.Spin()
}

func fatal(logger *slog.Logger, operation string, err error) {
	logger.Error("gateway startup failed", "operation", operation, "error", err.Error())
	os.Exit(1)
}
