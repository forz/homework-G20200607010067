package domain

import (
	"context"
	"encoding/json"
	"time"
)

// Protocol identifies the upstream wire protocol hidden by a model adapter.
type Protocol string

const (
	ProtocolOpenAIResponses   Protocol = "openai_responses"
	ProtocolAnthropicMessages Protocol = "anthropic_messages"
)

// Role is a supported text-message role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is the provider-neutral text message accepted by the gateway.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// JSONSchemaFormat describes the supported response_format payload.
type JSONSchemaFormat struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

// PromptReference selects a stored prompt version and its render variables.
type PromptReference struct {
	ID        string         `json:"id"`
	Version   *int           `json:"version,omitempty"`
	Variables map[string]any `json:"variables,omitempty"`
	Position  string         `json:"position,omitempty"`
}

// PromptMetadata records the actual prompt version used for a model call.
type PromptMetadata struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// ModelRequest is the request passed from orchestration to an adapter.
type ModelRequest struct {
	RequestID      string
	ModelAlias     string
	UpstreamModel  string
	Messages       []Message
	Temperature    *float32
	TopP           *float32
	MaxTokens      *int
	ResponseFormat *JSONSchemaFormat
	PromptRef      *PromptReference
	PromptMetadata *PromptMetadata
}

// TokenUsage is the common, privacy-safe usage representation.
type TokenUsage struct {
	InputTokens      int64          `json:"prompt_tokens"`
	OutputTokens     int64          `json:"completion_tokens"`
	TotalTokens      int64          `json:"total_tokens"`
	CachedTokens     int64          `json:"cached_tokens"`
	CacheWriteTokens int64          `json:"cache_write_tokens"`
	ReasoningTokens  int64          `json:"reasoning_tokens"`
	Incomplete       bool           `json:"incomplete"`
	ProviderMetadata map[string]any `json:"provider_metadata,omitempty"`
}

// ModelResponse is a normalized non-streaming model result.
type ModelResponse struct {
	Content      string
	FinishReason string
	Usage        TokenUsage
}

// StreamEventKind classifies normalized model stream events.
type StreamEventKind string

const (
	StreamEventDelta StreamEventKind = "delta"
	StreamEventUsage StreamEventKind = "usage"
	StreamEventDone  StreamEventKind = "done"
)

// StreamEvent is one provider-neutral model stream event.
type StreamEvent struct {
	Kind         StreamEventKind
	Delta        string
	FinishReason string
	Usage        *TokenUsage
}

// ModelStream is a closeable normalized provider stream.
type ModelStream interface {
	Recv() (*StreamEvent, error)
	Close() error
}

// ModelAdapter hides provider message, stream, usage, and error formats.
type ModelAdapter interface {
	Protocol() Protocol
	Generate(context.Context, *ModelRequest) (*ModelResponse, error)
	Stream(context.Context, *ModelRequest) (ModelStream, error)
}

// PromptVersion is one immutable stored prompt template revision.
type PromptVersion struct {
	ID          string    `json:"id"`
	Version     int       `json:"version"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Role        Role      `json:"role"`
	Content     string    `json:"content"`
	IsActive    bool      `json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
}

// PromptCreate carries fields used to create a new immutable prompt version.
type PromptCreate struct {
	ID          string
	Name        string
	Description string
	Role        Role
	Content     string
	Activate    bool
}

// UsageStatus is the terminal gateway call state persisted for observability.
type UsageStatus string

const (
	UsageSuccess   UsageStatus = "success"
	UsageError     UsageStatus = "error"
	UsageCancelled UsageStatus = "cancelled"
)

// UsageEvent is the privacy-safe terminal record for one gateway request.
type UsageEvent struct {
	RequestID             string         `json:"request_id"`
	CreatedAt             time.Time      `json:"created_at"`
	ModelAlias            string         `json:"model"`
	Protocol              Protocol       `json:"protocol"`
	UpstreamModel         string         `json:"upstream_model,omitempty"`
	Stream                bool           `json:"stream"`
	Status                UsageStatus    `json:"status"`
	HTTPStatus            int            `json:"http_status"`
	Usage                 TokenUsage     `json:"usage"`
	LatencyMS             float64        `json:"latency_ms"`
	FirstTokenMS          *float64       `json:"first_token_ms"`
	TransportRetries      int            `json:"transport_retries"`
	StructuredCorrections int            `json:"structured_corrections"`
	PromptID              string         `json:"prompt_id,omitempty"`
	PromptVersion         *int           `json:"prompt_version,omitempty"`
	ErrorType             string         `json:"error_type,omitempty"`
	ErrorCode             string         `json:"error_code,omitempty"`
	ProviderMetadata      map[string]any `json:"provider_metadata,omitempty"`
}
