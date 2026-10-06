package openai

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/homework-G20200607010067/week1/internal/config"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

// Adapter wraps Eino Ext's OpenAI Responses component behind the gateway's
// provider-neutral model boundary.
type Adapter struct {
	model      *agenticopenai.ResponsesModel
	baseConfig agenticopenai.ResponsesConfig
}

// New creates a Responses adapter that shares the process HTTP client and
// explicitly disables SDK retries so the gateway owns the complete retry budget.
func New(ctx context.Context, cfg config.ModelConfig, client *http.Client) (*Adapter, error) {
	noRetries := 0
	timeout := cfg.Timeout.Duration
	maxTokens := cfg.MaxTokens
	modelConfig := agenticopenai.ResponsesConfig{
		BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Model: cfg.UpstreamModel,
		HTTPClient: client, Timeout: &timeout, MaxRetries: &noRetries, MaxTokens: &maxTokens,
	}
	responsesModel, err := agenticopenai.NewResponsesModel(ctx, &modelConfig)
	if err != nil {
		return nil, err
	}
	return &Adapter{model: responsesModel, baseConfig: modelConfig}, nil
}

// Protocol identifies the upstream wire contract implemented by this adapter.
func (a *Adapter) Protocol() domain.Protocol { return domain.ProtocolOpenAIResponses }

// Generate performs exactly one Eino model call and normalizes the result.
func (a *Adapter) Generate(ctx context.Context, request *domain.ModelRequest) (*domain.ModelResponse, error) {
	messages, err := toAgenticMessages(request.Messages)
	if err != nil {
		return nil, err
	}
	selected, err := a.modelFor(ctx, request)
	if err != nil {
		return nil, err
	}
	result, err := selected.Generate(ctx, messages, modelOptions(request)...)
	if err != nil {
		return nil, err
	}
	return fromAgenticMessage(result)
}

func modelOptions(request *domain.ModelRequest) []einomodel.Option {
	options := make([]einomodel.Option, 0, 4)
	if request.UpstreamModel != "" {
		options = append(options, einomodel.WithModel(request.UpstreamModel))
	}
	if request.Temperature != nil {
		options = append(options, einomodel.WithTemperature(*request.Temperature))
	}
	if request.TopP != nil {
		options = append(options, einomodel.WithTopP(*request.TopP))
	}
	if request.MaxTokens != nil {
		options = append(options, einomodel.WithMaxTokens(*request.MaxTokens))
	}
	return options
}

func toAgenticMessages(messages []domain.Message) ([]*schema.AgenticMessage, error) {
	converted := make([]*schema.AgenticMessage, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case domain.RoleSystem, domain.RoleDeveloper:
			converted = append(converted, schema.SystemAgenticMessage(message.Content))
		case domain.RoleUser:
			converted = append(converted, schema.UserAgenticMessage(message.Content))
		case domain.RoleAssistant:
			converted = append(converted, &schema.AgenticMessage{
				Role: schema.AgenticRoleTypeAssistant,
				ContentBlocks: []*schema.ContentBlock{
					schema.NewContentBlock(&schema.AssistantGenText{Text: message.Content}),
				},
			})
		default:
			return nil, errors.New("unsupported role for OpenAI Responses adapter")
		}
	}
	return converted, nil
}

func fromAgenticMessage(message *schema.AgenticMessage) (*domain.ModelResponse, error) {
	if message == nil {
		return nil, errors.New("OpenAI Responses adapter returned a nil message")
	}
	if err := responseFailure(message.ResponseMeta); err != nil {
		return &domain.ModelResponse{Usage: openAITokenUsage(message.ResponseMeta)}, err
	}
	var content strings.Builder
	for _, block := range message.ContentBlocks {
		if block == nil || block.Type != schema.ContentBlockTypeAssistantGenText || block.AssistantGenText == nil {
			continue
		}
		content.WriteString(block.AssistantGenText.Text)
	}
	result := &domain.ModelResponse{
		Content: content.String(), FinishReason: openAIFinishReason(message),
		Usage: openAITokenUsage(message.ResponseMeta),
	}
	if content.Len() == 0 && result.FinishReason != "length" && result.FinishReason != "content_filter" {
		return result, core.NewError(http.StatusBadGateway, core.ErrorTypeUpstream,
			"upstream_empty_response", "upstream returned no assistant text")
	}
	return result, nil
}

func openAIFinishReason(message *schema.AgenticMessage) string {
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.OpenAIExtension == nil {
		return "unknown"
	}
	extension := message.ResponseMeta.OpenAIExtension
	if extension.IncompleteDetails != nil {
		if extension.IncompleteDetails.Reason == "content_filter" {
			return "content_filter"
		}
		return "length"
	}
	switch string(extension.Status) {
	case "completed":
		return "stop"
	case "incomplete":
		return "length"
	default:
		return "unknown"
	}
}

func responseFailure(meta *schema.AgenticResponseMeta) error {
	if meta != nil && meta.OpenAIExtension != nil {
		extension := meta.OpenAIExtension
		if string(extension.Status) == "failed" || extension.Error != nil {
			return core.NewError(http.StatusBadGateway, core.ErrorTypeUpstream,
				"upstream_response_failed", "upstream reported a failed response")
		}
	}
	return nil
}

func openAITokenUsage(meta *schema.AgenticResponseMeta) domain.TokenUsage {
	return normalizeAgenticUsage(meta)
}
