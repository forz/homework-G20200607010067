package api

import (
	"encoding/json"
	"fmt"

	"github.com/cloudwego/hertz/pkg/protocol/sse"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

type chatCompletionChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *Usage        `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type chunkDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

func encodeStreamEvent(id, model string, created int64, event *domain.StreamEvent) ([]byte, error) {
	chunk := chatCompletionChunk{ID: id, Object: "chat.completion.chunk", Created: created, Model: model}
	switch event.Kind {
	case domain.StreamEventDelta:
		chunk.Choices = []chunkChoice{{Index: 0, Delta: chunkDelta{Content: event.Delta}}}
	case domain.StreamEventUsage:
		chunk.Choices = []chunkChoice{}
		if event.Usage != nil {
			usage := toUsage(*event.Usage)
			chunk.Usage = &usage
		}
	case domain.StreamEventDone:
		reason := event.FinishReason
		chunk.Choices = []chunkChoice{{Index: 0, Delta: chunkDelta{}, FinishReason: &reason}}
	default:
		return nil, fmt.Errorf("unsupported stream event kind %q", event.Kind)
	}
	return json.Marshal(chunk)
}

func writeStreamError(writer *sse.Writer, requestID string, cause error) {
	payload, err := json.Marshal(streamErrorEnvelope(requestID, cause))
	if err != nil {
		return
	}
	if err := writer.WriteEvent("", "", payload); err != nil {
		return
	}
	_ = writer.WriteEvent("", "", []byte("[DONE]"))
}

func streamErrorEnvelope(requestID string, err error) ErrorEnvelope {
	gatewayErr := core.NormalizeError(err)
	return ErrorEnvelope{Error: ErrorDetail{
		Type: gatewayErr.Type, Code: gatewayErr.Code, Message: gatewayErr.Message,
		RequestID: requestID, Retryable: gatewayErr.Retryable, Param: gatewayErr.Param,
	}}
}
