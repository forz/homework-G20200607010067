package services

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/homework-G20200607010067/week1/internal/adapters"
	"github.com/homework-G20200607010067/week1/internal/config"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

// GatewayService orchestrates provider-neutral model calls.
type GatewayService struct {
	router               *ModelRouter
	maxSchemaBytes       int
	maxSchemaDepth       int
	maxStreamBufferBytes int
	prompts              *PromptService
	usageStore           UsageStore
	logger               *slog.Logger
	retryPolicy          core.RetryPolicy
	limiter              *core.ModelLimiter
}

// ConfigureResilience installs the single gateway retry budget and independent
// per-model token buckets.
func (s *GatewayService) ConfigureResilience(policy core.RetryPolicy, limiter *core.ModelLimiter) {
	s.retryPolicy = policy
	s.limiter = limiter
}

// ConfigurePrompts enables stored prompt references during request preparation.
func (s *GatewayService) ConfigurePrompts(prompts *PromptService) { s.prompts = prompts }

// NewGatewayService constructs the core application service.
func NewGatewayService(router *ModelRouter) *GatewayService {
	return &GatewayService{
		router: router, maxSchemaBytes: 64 << 10, maxSchemaDepth: 32, maxStreamBufferBytes: 1 << 20,
	}
}

// ConfigureLimits applies validated request bounds to orchestration.
func (s *GatewayService) ConfigureLimits(limits config.LimitsConfig) {
	s.maxSchemaBytes = limits.MaxSchemaBytes
	s.maxSchemaDepth = limits.MaxSchemaDepth
	s.maxStreamBufferBytes = limits.MaxStreamBufferBytes
}

// Models returns the stable public model aliases.
func (s *GatewayService) Models() []string {
	if s == nil {
		return nil
	}
	return s.router.Models()
}

// Generate routes one validated client request to exactly one adapter and
// preserves the gateway-owned request ID in the orchestration boundary.
func (s *GatewayService) Generate(ctx context.Context, request *domain.ModelRequest) (response *domain.ModelResponse, callErr error) {
	if s == nil || request == nil {
		return nil, errors.New("gateway service received a nil request")
	}
	started := time.Now()
	usageEvent := newUsageEvent(request, false, started)
	transportRetries := 0
	defer func() {
		usageEvent.TransportRetries = transportRetries
		finishUsageEvent(&usageEvent, started, response, callErr)
		s.persistUsage(usageEvent)
	}()
	route, err := s.router.Resolve(request.ModelAlias)
	if err != nil {
		return nil, err
	}
	usageEvent.Protocol = route.Adapter.Protocol()
	usageEvent.UpstreamModel = route.UpstreamModel
	prepared, err := s.prepareRequest(ctx, request)
	if err != nil {
		return nil, err
	}
	if prepared.PromptMetadata != nil {
		usageEvent.PromptID = prepared.PromptMetadata.ID
		version := prepared.PromptMetadata.Version
		usageEvent.PromptVersion = &version
	}
	upstreamRequest := *prepared
	upstreamRequest.UpstreamModel = route.UpstreamModel
	validator, err := compileStructured(request.ResponseFormat, s.maxSchemaBytes, s.maxSchemaDepth)
	if err != nil {
		return nil, err
	}
	if s.limiter != nil && !s.limiter.Allow(request.ModelAlias) {
		return nil, core.NewError(
			http.StatusTooManyRequests, core.ErrorTypeRateLimit, "model_rate_limited",
			"model request rate limit exceeded",
		).WithParam("model").WithRetryable(true)
	}
	result, retries, err := s.generateWithRetry(ctx, route.Adapter, &upstreamRequest, s.retryPolicy.MaxRetries)
	transportRetries += retries
	if result != nil {
		usageEvent.Usage = result.Usage
	} else if err != nil {
		usageEvent.Usage.Incomplete = true
	}
	if err != nil || validator == nil {
		return result, err
	}
	if validationErr := validator.ValidateText(result.Content); validationErr == nil {
		return result, nil
	}
	usageEvent.StructuredCorrections = 1
	correction := upstreamRequest
	correction.Messages = append(append([]domain.Message(nil), upstreamRequest.Messages...), domain.Message{
		Role:    domain.RoleDeveloper,
		Content: "The previous answer was invalid. Return only one JSON value that strictly matches the requested schema.",
	})
	corrected, correctionRetries, correctionErr := s.generateWithRetry(ctx, route.Adapter, &correction, s.retryPolicy.MaxRetries-transportRetries)
	transportRetries += correctionRetries
	if corrected != nil {
		corrected.Usage = sumUsage(result.Usage, corrected.Usage)
		usageEvent.Usage = corrected.Usage
	}
	if correctionErr != nil {
		if corrected == nil {
			usageEvent.Usage.Incomplete = true
		}
		return nil, correctionErr
	}
	if validationErr := validator.ValidateText(corrected.Content); validationErr != nil {
		return nil, core.WrapError(
			http.StatusUnprocessableEntity, core.ErrorTypeUpstream, "invalid_structured_output",
			"model output did not match the requested JSON schema after one correction", validationErr,
		)
	}
	return corrected, nil
}

func (s *GatewayService) generateWithRetry(
	ctx context.Context,
	adapter domain.ModelAdapter,
	request *domain.ModelRequest,
	maxRetries int,
) (result *domain.ModelResponse, retries int, err error) {
	policy := s.retryPolicy
	policy.MaxRetries = maxRetries
	retries, err = policy.Do(ctx, func(_ int) error {
		result, err = adapter.Generate(ctx, request)
		return err
	}, adapters.IsRetryable)
	if err != nil {
		return result, retries, adapters.NormalizeUpstreamError(err)
	}
	return result, retries, nil
}

func (s *GatewayService) prepareRequest(ctx context.Context, request *domain.ModelRequest) (*domain.ModelRequest, error) {
	prepared := *request
	prepared.Messages = append([]domain.Message(nil), request.Messages...)
	if request.PromptRef == nil {
		return &prepared, nil
	}
	if s.prompts == nil {
		return nil, core.NewError(http.StatusInternalServerError, core.ErrorTypeInternal, "prompt_service_unavailable", "prompt service is unavailable")
	}
	rendered, err := s.prompts.Render(ctx, request.PromptRef.ID, request.PromptRef.Version, request.PromptRef.Variables)
	if err != nil {
		return nil, err
	}
	message := domain.Message{Role: rendered.Role, Content: rendered.Content}
	if request.PromptRef.Position == "append" {
		prepared.Messages = append(prepared.Messages, message)
	} else {
		prepared.Messages = append([]domain.Message{message}, prepared.Messages...)
	}
	prepared.PromptMetadata = &domain.PromptMetadata{ID: rendered.ID, Version: rendered.Version}
	return &prepared, nil
}

func sumUsage(left, right domain.TokenUsage) domain.TokenUsage {
	right.InputTokens += left.InputTokens
	right.OutputTokens += left.OutputTokens
	right.TotalTokens += left.TotalTokens
	right.CachedTokens += left.CachedTokens
	right.CacheWriteTokens += left.CacheWriteTokens
	right.ReasoningTokens += left.ReasoningTokens
	right.Incomplete = left.Incomplete || right.Incomplete
	return right
}
