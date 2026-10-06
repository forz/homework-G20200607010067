package anthropic

import (
	"context"
	"io"
	"net/http"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

func (a *Adapter) Stream(ctx context.Context, request *domain.ModelRequest) (domain.ModelStream, error) {
	params, err := a.parameters(request)
	if err != nil {
		return nil, err
	}
	reader := a.messages.NewStreaming(ctx, params)
	if err := reader.Err(); err != nil {
		_ = reader.Close()
		return nil, err
	}
	return &messageStream{reader: reader}, nil
}

type messageReader interface {
	Next() bool
	Current() sdk.MessageStreamEventUnion
	Err() error
	Close() error
}

type messageStream struct {
	reader       messageReader
	usage        sdk.Usage
	finishReason string
	started      bool
	stopped      bool
	terminalStep int
}

func (s *messageStream) Recv() (*domain.StreamEvent, error) {
	if s.stopped {
		return s.terminal()
	}
	for s.reader.Next() {
		event := s.reader.Current()
		switch event.Type {
		case "message_start":
			s.started = true
			s.usage = event.Message.Usage
		case "content_block_start":
			if event.ContentBlock.Type == "text" && event.ContentBlock.Text != "" {
				return s.delta(event.ContentBlock.Text), nil
			}
		case "content_block_delta":
			if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
				return s.delta(event.Delta.Text), nil
			}
		case "message_delta":
			mergeUsage(&s.usage, event.Usage)
			if event.Delta.StopReason != "" {
				s.finishReason = finishReason(string(event.Delta.StopReason))
			}
		case "message_stop":
			if !s.started || s.finishReason == "" {
				return nil, incompleteStream()
			}
			s.stopped = true
			return s.terminal()
		}
	}
	if err := s.reader.Err(); err != nil {
		return nil, err
	}
	return nil, incompleteStream()
}

func (s *messageStream) delta(text string) *domain.StreamEvent {
	usage := normalizeUsage(s.usage)
	usage.Incomplete = true
	return &domain.StreamEvent{Kind: domain.StreamEventDelta, Delta: text, Usage: &usage}
}

func (s *messageStream) terminal() (*domain.StreamEvent, error) {
	switch s.terminalStep {
	case 0:
		s.terminalStep++
		usage := normalizeUsage(s.usage)
		usage.ProviderMetadata = map[string]any{"finish_reason": s.finishReason}
		return &domain.StreamEvent{Kind: domain.StreamEventUsage, Usage: &usage}, nil
	case 1:
		s.terminalStep++
		return &domain.StreamEvent{Kind: domain.StreamEventDone, FinishReason: s.finishReason}, nil
	default:
		return nil, io.EOF
	}
}

func (s *messageStream) Close() error { return s.reader.Close() }

func incompleteStream() error {
	return core.NewError(http.StatusBadGateway, core.ErrorTypeUpstream,
		"upstream_stream_incomplete", "upstream stream ended without a terminal event")
}
