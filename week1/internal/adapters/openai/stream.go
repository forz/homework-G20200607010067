package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

// Stream starts exactly one OpenAI Responses stream through Eino Ext.
func (a *Adapter) Stream(ctx context.Context, request *domain.ModelRequest) (domain.ModelStream, error) {
	messages, err := toAgenticMessages(request.Messages)
	if err != nil {
		return nil, err
	}
	selected, err := a.modelFor(ctx, request)
	if err != nil {
		return nil, err
	}
	reader, err := selected.Stream(ctx, messages, modelOptions(request)...)
	if err != nil {
		return nil, err
	}
	return &responseStream{reader: reader, finishReason: "stop"}, nil
}

type responseStream struct {
	reader       *schema.StreamReader[*schema.AgenticMessage]
	usage        *domain.TokenUsage
	finishReason string
	terminalStep int
	stopped      bool
	terminalErr  error
}

func (s *responseStream) Recv() (*domain.StreamEvent, error) {
	if s.stopped {
		return s.terminal()
	}
	for {
		message, err := s.reader.Recv()
		if errors.Is(err, io.EOF) {
			return nil, core.NewError(http.StatusBadGateway, core.ErrorTypeUpstream,
				"upstream_stream_incomplete", "upstream stream ended without a terminal event")
		}
		if err != nil {
			return nil, err
		}
		if message == nil {
			continue
		}
		if message.ResponseMeta != nil && message.ResponseMeta.TokenUsage != nil {
			usage := openAITokenUsage(message.ResponseMeta)
			s.usage = &usage
		}
		if reason := openAIFinishReason(message); reason != "unknown" {
			s.finishReason = reason
		}
		if meta := message.ResponseMeta; meta != nil && meta.OpenAIExtension != nil {
			switch string(meta.OpenAIExtension.Status) {
			case "completed", "incomplete", "failed":
				s.stopped = true
				s.terminalErr = responseFailure(meta)
			}
		}
		if delta := agenticText(message); delta != "" {
			return &domain.StreamEvent{Kind: domain.StreamEventDelta, Delta: delta, Usage: s.usage}, nil
		}
		if s.stopped {
			return s.terminal()
		}
	}
}

func (s *responseStream) terminal() (*domain.StreamEvent, error) {
	if s.terminalStep == 0 && s.usage != nil {
		s.terminalStep++
		return &domain.StreamEvent{Kind: domain.StreamEventUsage, Usage: s.usage}, nil
	}
	if s.terminalErr != nil && s.terminalStep < 2 {
		s.terminalStep = 2
		return nil, s.terminalErr
	}
	if s.terminalStep <= 1 {
		s.terminalStep = 2
		return &domain.StreamEvent{Kind: domain.StreamEventDone, FinishReason: s.finishReason}, nil
	}
	return nil, io.EOF
}

func (s *responseStream) Close() error {
	s.reader.Close()
	return nil
}

func agenticText(message *schema.AgenticMessage) string {
	var builder strings.Builder
	for _, block := range message.ContentBlocks {
		if block != nil && block.Type == schema.ContentBlockTypeAssistantGenText && block.AssistantGenText != nil {
			builder.WriteString(block.AssistantGenText.Text)
		}
	}
	return builder.String()
}
