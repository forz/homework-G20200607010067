package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/homework-G20200607010067/week1/internal/adapters"
	"github.com/homework-G20200607010067/week1/internal/config"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

func TestGatewayServiceGenerate(t *testing.T) {
	t.Run("retries transport errors and records terminal usage once", func(t *testing.T) {
		calls := 0
		adapter := &fakeAdapter{protocol: domain.ProtocolOpenAIResponses}
		adapter.generate = func(context.Context, *domain.ModelRequest) (*domain.ModelResponse, error) {
			calls++
			if calls < 3 {
				return nil, &adapters.HTTPStatusError{StatusCode: http.StatusInternalServerError}
			}
			return &domain.ModelResponse{Content: "ok", FinishReason: "stop", Usage: domain.TokenUsage{TotalTokens: 7}}, nil
		}
		service := newGatewayForTest(t, adapter)
		usage := &usageCapture{}
		service.ConfigureUsage(usage, slog.Default())
		service.ConfigureResilience(core.RetryPolicy{MaxRetries: 3}, nil)

		response, err := service.Generate(context.Background(), &domain.ModelRequest{RequestID: "req_retry", ModelAlias: "model", Messages: []domain.Message{{Role: domain.RoleUser, Content: "hello"}}})
		if err != nil || response.Content != "ok" || calls != 3 {
			t.Fatalf("Generate() response=%#v err=%v calls=%d", response, err, calls)
		}
		if len(usage.events) != 1 || usage.events[0].TransportRetries != 2 || usage.events[0].Status != domain.UsageSuccess || usage.events[0].Protocol != domain.ProtocolOpenAIResponses {
			t.Fatalf("terminal usage=%#v", usage.events)
		}
	})

	t.Run("performs exactly one structured correction and sums usage", func(t *testing.T) {
		calls := 0
		adapter := &fakeAdapter{protocol: domain.ProtocolOpenAIResponses}
		adapter.generate = func(_ context.Context, request *domain.ModelRequest) (*domain.ModelResponse, error) {
			calls++
			if calls == 1 {
				return &domain.ModelResponse{Content: `{"name":1}`, Usage: domain.TokenUsage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}, nil
			}
			if got := request.Messages[len(request.Messages)-1].Role; got != domain.RoleDeveloper {
				t.Fatalf("correction role=%q, want developer", got)
			}
			return &domain.ModelResponse{Content: `{"name":"Alice"}`, Usage: domain.TokenUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
		}
		service := newGatewayForTest(t, adapter)
		usage := &usageCapture{}
		service.ConfigureUsage(usage, slog.Default())
		response, err := service.Generate(context.Background(), &domain.ModelRequest{
			RequestID: "req_structured", ModelAlias: "model",
			Messages:       []domain.Message{{Role: domain.RoleUser, Content: "extract"}},
			ResponseFormat: &domain.JSONSchemaFormat{Name: "person", Strict: true, Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)},
		})
		if err != nil || calls != 2 || response.Usage.TotalTokens != 8 || response.Usage.InputTokens != 5 || response.Usage.OutputTokens != 3 {
			t.Fatalf("structured Generate() response=%#v err=%v calls=%d", response, err, calls)
		}
		if len(usage.events) != 1 || usage.events[0].StructuredCorrections != 1 {
			t.Fatalf("structured usage=%#v", usage.events)
		}
	})
}

func TestGatewayServiceStream(t *testing.T) {
	t.Run("retries before first text and records TTFT", func(t *testing.T) {
		first := &fakeStream{results: []streamResult{{err: &adapters.HTTPStatusError{StatusCode: http.StatusInternalServerError}}}}
		second := &fakeStream{results: []streamResult{
			{event: &domain.StreamEvent{Kind: domain.StreamEventDelta, Delta: "hello"}},
			{event: &domain.StreamEvent{Kind: domain.StreamEventUsage, Usage: &domain.TokenUsage{TotalTokens: 4}}},
			{event: &domain.StreamEvent{Kind: domain.StreamEventDone, FinishReason: "stop"}},
		}}
		calls := 0
		adapter := &fakeAdapter{protocol: domain.ProtocolAnthropicMessages}
		adapter.stream = func(context.Context, *domain.ModelRequest) (domain.ModelStream, error) {
			calls++
			if calls == 1 {
				return first, nil
			}
			return second, nil
		}
		service := newGatewayForTest(t, adapter)
		usage := &usageCapture{}
		service.ConfigureUsage(usage, slog.Default())
		service.ConfigureResilience(core.RetryPolicy{MaxRetries: 1}, nil)
		stream, err := service.Stream(context.Background(), &domain.ModelRequest{RequestID: "req_stream", ModelAlias: "model", Messages: []domain.Message{{Role: domain.RoleUser, Content: "hello"}}})
		if err != nil {
			t.Fatalf("Stream() error=%v", err)
		}
		defer stream.Close()
		for _, kind := range []domain.StreamEventKind{domain.StreamEventDelta, domain.StreamEventUsage, domain.StreamEventDone} {
			event, recvErr := stream.Recv()
			if recvErr != nil || event.Kind != kind {
				t.Fatalf("Recv() event=%#v err=%v, want kind=%q", event, recvErr, kind)
			}
		}
		if calls != 2 || !first.closed {
			t.Fatalf("stream retry calls=%d firstClosed=%v", calls, first.closed)
		}
		if len(usage.events) != 1 || usage.events[0].TransportRetries != 1 || usage.events[0].FirstTokenMS == nil || usage.events[0].Usage.TotalTokens != 4 {
			t.Fatalf("stream usage=%#v", usage.events)
		}
	})

	t.Run("does not retry after text was emitted", func(t *testing.T) {
		inner := &fakeStream{results: []streamResult{
			{event: &domain.StreamEvent{Kind: domain.StreamEventDelta, Delta: "partial"}},
			{err: &adapters.HTTPStatusError{StatusCode: http.StatusInternalServerError}},
		}}
		calls := 0
		adapter := &fakeAdapter{protocol: domain.ProtocolOpenAIResponses, stream: func(context.Context, *domain.ModelRequest) (domain.ModelStream, error) {
			calls++
			return inner, nil
		}}
		service := newGatewayForTest(t, adapter)
		service.ConfigureResilience(core.RetryPolicy{MaxRetries: 3}, nil)
		stream, err := service.Stream(context.Background(), &domain.ModelRequest{RequestID: "req_partial", ModelAlias: "model", Messages: []domain.Message{{Role: domain.RoleUser, Content: "hello"}}})
		if err != nil {
			t.Fatalf("Stream() error=%v", err)
		}
		defer stream.Close()
		if event, recvErr := stream.Recv(); recvErr != nil || event.Delta != "partial" {
			t.Fatalf("first Recv() event=%#v err=%v", event, recvErr)
		}
		if _, recvErr := stream.Recv(); recvErr == nil || core.NormalizeError(recvErr).Code != "upstream_failure" {
			t.Fatalf("post-text Recv() error=%v, want upstream_failure", recvErr)
		}
		if calls != 1 {
			t.Fatalf("adapter stream calls=%d, want 1", calls)
		}
	})
}

func TestCompileStructured(t *testing.T) {
	format := &domain.JSONSchemaFormat{Name: "person", Strict: true, Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`)}
	validator, err := compileStructured(format, 1024, 16)
	if err != nil {
		t.Fatalf("compileStructured() error=%v", err)
	}
	if err := validator.ValidateText(`{"name":"Alice"}`); err != nil {
		t.Fatalf("valid structured result rejected: %v", err)
	}
	for _, content := range []string{`not-json`, `{"name":1}`, `{"name":"Alice"} {"name":"Bob"}`} {
		if err := validator.ValidateText(content); err == nil || core.NormalizeError(err).Code != "invalid_structured_output" {
			t.Fatalf("invalid content %q error=%v", content, err)
		}
	}
	if _, err := compileStructured(format, 4, 16); err == nil || core.NormalizeError(err).Code != "invalid_json_schema" {
		t.Fatalf("oversized schema error=%v", err)
	}
	if _, err := compileStructured(format, 1024, 2); err == nil || core.NormalizeError(err).Code != "invalid_json_schema" {
		t.Fatalf("overdeep schema error=%v", err)
	}
}

func newGatewayForTest(t *testing.T, adapter domain.ModelAdapter) *GatewayService {
	t.Helper()
	router, err := NewModelRouter(map[string]config.ModelConfig{
		"model": {Protocol: adapter.Protocol(), UpstreamModel: "upstream"},
	}, map[string]domain.ModelAdapter{"model": adapter})
	if err != nil {
		t.Fatalf("NewModelRouter() error=%v", err)
	}
	return NewGatewayService(router)
}

type fakeAdapter struct {
	protocol domain.Protocol
	generate func(context.Context, *domain.ModelRequest) (*domain.ModelResponse, error)
	stream   func(context.Context, *domain.ModelRequest) (domain.ModelStream, error)
}

func (a *fakeAdapter) Protocol() domain.Protocol { return a.protocol }
func (a *fakeAdapter) Generate(ctx context.Context, request *domain.ModelRequest) (*domain.ModelResponse, error) {
	if a.generate == nil {
		return nil, errors.New("generate not configured")
	}
	return a.generate(ctx, request)
}
func (a *fakeAdapter) Stream(ctx context.Context, request *domain.ModelRequest) (domain.ModelStream, error) {
	if a.stream == nil {
		return nil, errors.New("stream not configured")
	}
	return a.stream(ctx, request)
}

type streamResult struct {
	event *domain.StreamEvent
	err   error
}

type fakeStream struct {
	results []streamResult
	index   int
	closed  bool
}

func (s *fakeStream) Recv() (*domain.StreamEvent, error) {
	if s.index >= len(s.results) {
		return nil, io.EOF
	}
	result := s.results[s.index]
	s.index++
	return result.event, result.err
}

func (s *fakeStream) Close() error {
	s.closed = true
	return nil
}

type usageCapture struct {
	events []domain.UsageEvent
}

func (s *usageCapture) Insert(_ context.Context, event domain.UsageEvent) error {
	s.events = append(s.events, event)
	return nil
}
