package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/homework-G20200607010067/week1/internal/config"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

const RequestIDHeader = "X-Request-ID"

var safeIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var schemaName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatCompletionRequest struct {
	Model          string                 `json:"model"`
	Messages       []ChatMessage          `json:"messages"`
	Stream         bool                   `json:"stream,omitempty"`
	Temperature    *float32               `json:"temperature,omitempty"`
	TopP           *float32               `json:"top_p,omitempty"`
	MaxTokens      *int                   `json:"max_tokens,omitempty"`
	ResponseFormat *ResponseFormatRequest `json:"response_format,omitempty"`
	PromptRef      *PromptReference       `json:"prompt_ref,omitempty"`
}

type ResponseFormatRequest struct {
	Type       string            `json:"type"`
	JSONSchema JSONSchemaRequest `json:"json_schema"`
}

type JSONSchemaRequest struct {
	Name   string          `json:"name"`
	Strict *bool           `json:"strict,omitempty"`
	Schema json.RawMessage `json:"schema"`
}

type PromptReference struct {
	ID        string         `json:"id"`
	Version   *int           `json:"version,omitempty"`
	Variables map[string]any `json:"variables,omitempty"`
	Position  string         `json:"position,omitempty"`
}

type ErrorEnvelope struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Type      string `json:"type"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Retryable bool   `json:"retryable"`
	Param     string `json:"param,omitempty"`
}

type ChatCompletion struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []CompletionChoice `json:"choices"`
	Usage   Usage              `json:"usage"`
}

type CompletionChoice struct {
	Index        int              `json:"index"`
	Message      AssistantMessage `json:"message"`
	FinishReason string           `json:"finish_reason"`
}

type AssistantMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Usage struct {
	PromptTokens           int64                  `json:"prompt_tokens"`
	CompletionTokens       int64                  `json:"completion_tokens"`
	TotalTokens            int64                  `json:"total_tokens"`
	PromptTokensDetails    PromptTokenDetails     `json:"prompt_tokens_details"`
	CompletionTokenDetails CompletionTokenDetails `json:"completion_tokens_details"`
	Incomplete             bool                   `json:"incomplete,omitempty"`
	ProviderMetadata       map[string]any         `json:"provider_metadata,omitempty"`
}

type PromptTokenDetails struct {
	CachedTokens     int64 `json:"cached_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

type CompletionTokenDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

type ModelList struct {
	Object string        `json:"object"`
	Data   []ModelObject `json:"data"`
}

type ModelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type PromptCreateRequest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Role        string `json:"role,omitempty"`
	Content     string `json:"content"`
	Activate    *bool  `json:"activate,omitempty"`
}

type PromptRenderRequest struct {
	Version   *int           `json:"version,omitempty"`
	Variables map[string]any `json:"variables,omitempty"`
}

type PromptList struct {
	Data []domain.PromptVersion `json:"data"`
}

// ValidateChatRequest rejects unsupported or unbounded input before rate limiting.
func ValidateChatRequest(request *ChatCompletionRequest, limits config.LimitsConfig) *core.GatewayError {
	if request == nil {
		return invalid("invalid_request", "request body is required", "")
	}
	if strings.TrimSpace(request.Model) == "" {
		return invalid("missing_model", "model is required", "model")
	}
	if len(request.Messages) == 0 || len(request.Messages) > limits.MaxMessages {
		return invalid("invalid_messages", "messages must contain a bounded non-empty list", "messages")
	}
	for _, message := range request.Messages {
		if !validRole(message.Role) {
			return invalid("unsupported_role", "message role is not supported", "messages.role")
		}
		if message.Content == "" || len(message.Content) > limits.MaxMessageBytes {
			return invalid("invalid_message_content", "message content is empty or too large", "messages.content")
		}
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 1) {
		return invalid("invalid_temperature", "temperature must be between 0 and 1", "temperature")
	}
	if request.TopP != nil && (*request.TopP < 0 || *request.TopP > 1) {
		return invalid("invalid_top_p", "top_p must be between 0 and 1", "top_p")
	}
	if request.MaxTokens != nil && *request.MaxTokens < 1 {
		return invalid("invalid_max_tokens", "max_tokens must be positive", "max_tokens")
	}
	if err := validateResponseFormat(request.ResponseFormat, limits); err != nil {
		return err
	}
	if err := validatePromptReference(request.PromptRef); err != nil {
		return err
	}
	return nil
}

// ToModelRequest converts a validated transport DTO to the provider-neutral request.
func (r *ChatCompletionRequest) ToModelRequest(requestID string) *domain.ModelRequest {
	messages := make([]domain.Message, 0, len(r.Messages))
	for _, message := range r.Messages {
		messages = append(messages, domain.Message{Role: domain.Role(message.Role), Content: message.Content})
	}
	request := &domain.ModelRequest{
		RequestID: requestID, ModelAlias: r.Model, Messages: messages,
		Temperature: r.Temperature, TopP: r.TopP, MaxTokens: r.MaxTokens,
	}
	if r.ResponseFormat != nil {
		strict := true
		if r.ResponseFormat.JSONSchema.Strict != nil {
			strict = *r.ResponseFormat.JSONSchema.Strict
		}
		request.ResponseFormat = &domain.JSONSchemaFormat{
			Name: r.ResponseFormat.JSONSchema.Name, Strict: strict,
			Schema: append(json.RawMessage(nil), r.ResponseFormat.JSONSchema.Schema...),
		}
	}
	if r.PromptRef != nil {
		request.PromptRef = &domain.PromptReference{
			ID: r.PromptRef.ID, Version: r.PromptRef.Version,
			Variables: r.PromptRef.Variables, Position: r.PromptRef.Position,
		}
	}
	return request
}

func validateResponseFormat(format *ResponseFormatRequest, limits config.LimitsConfig) *core.GatewayError {
	if format == nil {
		return nil
	}
	if format.Type != "json_schema" {
		return invalid("unsupported_response_format", "only json_schema response_format is supported", "response_format.type")
	}
	if !schemaName.MatchString(format.JSONSchema.Name) {
		return invalid("invalid_schema_name", "json_schema name is invalid", "response_format.json_schema.name")
	}
	if len(format.JSONSchema.Schema) == 0 || len(format.JSONSchema.Schema) > limits.MaxSchemaBytes || !json.Valid(format.JSONSchema.Schema) {
		return invalid("invalid_json_schema", "json_schema is invalid or too large", "response_format.json_schema.schema")
	}
	return nil
}

func validatePromptReference(reference *PromptReference) *core.GatewayError {
	if reference == nil {
		return nil
	}
	if !safeIdentifier.MatchString(reference.ID) {
		return invalid("invalid_prompt_id", "prompt id is invalid", "prompt_ref.id")
	}
	if reference.Version != nil && *reference.Version < 1 {
		return invalid("invalid_prompt_version", "prompt version must be positive", "prompt_ref.version")
	}
	if reference.Position != "" && reference.Position != "prepend" && reference.Position != "append" {
		return invalid("invalid_prompt_position", "prompt position must be prepend or append", "prompt_ref.position")
	}
	for _, value := range reference.Variables {
		switch value.(type) {
		case nil, string, float64, bool:
		default:
			return invalid("invalid_prompt_variable", "prompt variables must be scalar JSON values", "prompt_ref.variables")
		}
	}
	return nil
}

func validRole(role string) bool {
	switch domain.Role(role) {
	case domain.RoleSystem, domain.RoleDeveloper, domain.RoleUser, domain.RoleAssistant:
		return true
	default:
		return false
	}
}

func invalid(code, message, param string) *core.GatewayError {
	errorValue := core.NewError(http.StatusBadRequest, core.ErrorTypeInvalidRequest, code, message)
	if param != "" {
		return errorValue.WithParam(param)
	}
	return errorValue
}

func toUsage(usage domain.TokenUsage) Usage {
	return Usage{
		PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens,
		TotalTokens: usage.TotalTokens, Incomplete: usage.Incomplete,
		PromptTokensDetails: PromptTokenDetails{
			CachedTokens: usage.CachedTokens, CacheWriteTokens: usage.CacheWriteTokens,
		},
		CompletionTokenDetails: CompletionTokenDetails{ReasoningTokens: usage.ReasoningTokens},
		ProviderMetadata:       usage.ProviderMetadata,
	}
}
