package openai

import (
	"github.com/cloudwego/eino/schema"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

func normalizeAgenticUsage(meta *schema.AgenticResponseMeta) domain.TokenUsage {
	if meta == nil || meta.TokenUsage == nil {
		return domain.TokenUsage{Incomplete: true}
	}
	usage := meta.TokenUsage
	normalized := domain.TokenUsage{
		InputTokens: int64(usage.PromptTokens), OutputTokens: int64(usage.CompletionTokens),
		TotalTokens: int64(usage.TotalTokens), CachedTokens: int64(usage.PromptTokenDetails.CachedTokens),
		ReasoningTokens: int64(usage.CompletionTokensDetails.ReasoningTokens),
	}
	if normalized.TotalTokens == 0 {
		normalized.TotalTokens = normalized.InputTokens + normalized.OutputTokens
	}
	if meta.OpenAIExtension != nil {
		normalized.ProviderMetadata = map[string]any{
			"status": string(meta.OpenAIExtension.Status), "service_tier": string(meta.OpenAIExtension.ServiceTier),
		}
	}
	return normalized
}
