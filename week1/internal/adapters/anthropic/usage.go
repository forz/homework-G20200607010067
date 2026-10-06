package anthropic

import (
	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

func normalizeUsage(usage sdk.Usage) domain.TokenUsage {
	input := usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	return domain.TokenUsage{
		InputTokens: input, OutputTokens: usage.OutputTokens, TotalTokens: input + usage.OutputTokens,
		CachedTokens: usage.CacheReadInputTokens, CacheWriteTokens: usage.CacheCreationInputTokens,
		ReasoningTokens: usage.OutputTokensDetails.ThinkingTokens,
		Incomplete:      !usage.JSON.InputTokens.Valid() || !usage.JSON.OutputTokens.Valid(),
	}
}

// Delta counts are cumulative, but fields absent from a delta retain their
// message_start values. Presence checks also preserve an explicit zero update.
func mergeUsage(usage *sdk.Usage, delta sdk.MessageDeltaUsage) {
	if delta.JSON.InputTokens.Valid() {
		usage.InputTokens = delta.InputTokens
		usage.JSON.InputTokens = delta.JSON.InputTokens
	}
	if delta.JSON.OutputTokens.Valid() {
		usage.OutputTokens = delta.OutputTokens
		usage.JSON.OutputTokens = delta.JSON.OutputTokens
	}
	if delta.JSON.CacheReadInputTokens.Valid() {
		usage.CacheReadInputTokens = delta.CacheReadInputTokens
	}
	if delta.JSON.CacheCreationInputTokens.Valid() {
		usage.CacheCreationInputTokens = delta.CacheCreationInputTokens
	}
	if delta.JSON.OutputTokensDetails.Valid() {
		usage.OutputTokensDetails = delta.OutputTokensDetails
	}
}
