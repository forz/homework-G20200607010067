package anthropic

import (
	"context"
	"errors"
	"net/http"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/homework-G20200607010067/week1/internal/config"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

// Adapter owns the Messages wire contract; the gateway owns all retries.
type Adapter struct {
	messages  sdk.MessageService
	model     string
	maxTokens int64
}

// New uses explicit options so unrelated Anthropic environment credentials
// cannot override the gateway configuration. SDK retries are always disabled.
func New(_ context.Context, cfg config.ModelConfig, client *http.Client) (*Adapter, error) {
	return &Adapter{
		messages: sdk.NewMessageService(
			option.WithEnvironmentProduction(), option.WithBaseURL(cfg.BaseURL),
			option.WithAPIKey(cfg.APIKey), option.WithHTTPClient(client),
			option.WithRequestTimeout(cfg.Timeout.Duration), option.WithMaxRetries(0),
		),
		model: cfg.UpstreamModel, maxTokens: int64(cfg.MaxTokens),
	}, nil
}

func (a *Adapter) Protocol() domain.Protocol { return domain.ProtocolAnthropicMessages }

func (a *Adapter) Generate(ctx context.Context, request *domain.ModelRequest) (*domain.ModelResponse, error) {
	params, err := a.parameters(request)
	if err != nil {
		return nil, err
	}
	message, err := a.messages.New(ctx, params)
	if err != nil {
		return nil, err
	}
	return fromMessage(message)
}

func (a *Adapter) parameters(request *domain.ModelRequest) (sdk.MessageNewParams, error) {
	params := sdk.MessageNewParams{Model: sdk.Model(a.model), MaxTokens: a.maxTokens}
	if request.UpstreamModel != "" {
		params.Model = sdk.Model(request.UpstreamModel)
	}
	if request.MaxTokens != nil {
		params.MaxTokens = int64(*request.MaxTokens)
	}
	if request.Temperature != nil {
		params.Temperature = sdk.Float(float64(*request.Temperature))
	}
	if request.TopP != nil {
		params.TopP = sdk.Float(float64(*request.TopP))
	}
	for _, message := range request.Messages {
		switch message.Role {
		case domain.RoleSystem, domain.RoleDeveloper:
			params.System = append(params.System, sdk.TextBlockParam{Text: message.Content})
		case domain.RoleUser:
			params.Messages = append(params.Messages, sdk.NewUserMessage(sdk.NewTextBlock(message.Content)))
		case domain.RoleAssistant:
			params.Messages = append(params.Messages, sdk.NewAssistantMessage(sdk.NewTextBlock(message.Content)))
		default:
			return params, errors.New("unsupported role for Anthropic Messages adapter")
		}
	}
	if err := applyStructured(&params, request.ResponseFormat); err != nil {
		return params, err
	}
	return params, nil
}

func fromMessage(message *sdk.Message) (*domain.ModelResponse, error) {
	if message == nil {
		return nil, errors.New("Anthropic Messages adapter returned a nil message")
	}
	var content strings.Builder
	for _, block := range message.Content {
		if block.Type == "text" {
			content.WriteString(block.Text)
		}
	}
	result := &domain.ModelResponse{
		Content: content.String(), FinishReason: finishReason(string(message.StopReason)),
		Usage: normalizeUsage(message.Usage),
	}
	if content.Len() == 0 && result.FinishReason != "length" && result.FinishReason != "content_filter" {
		return result, core.NewError(http.StatusBadGateway, core.ErrorTypeUpstream,
			"upstream_empty_response", "upstream returned no assistant text")
	}
	return result, nil
}

func finishReason(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "refusal":
		return "content_filter"
	default:
		return "unknown"
	}
}
