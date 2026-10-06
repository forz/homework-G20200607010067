package anthropic

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

func TestMessageParameters(t *testing.T) {
	adapter := &Adapter{model: "upstream", maxTokens: 100}
	params, err := adapter.parameters(&domain.ModelRequest{Messages: []domain.Message{
		{Role: domain.RoleSystem, Content: "system"},
		{Role: domain.RoleDeveloper, Content: "developer"},
		{Role: domain.RoleUser, Content: "user"},
		{Role: domain.RoleAssistant, Content: "assistant"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(params.System) != 2 || len(params.Messages) != 2 || params.Messages[0].Role != "user" || params.Messages[1].Role != "assistant" {
		t.Fatalf("unexpected Messages role mapping: %#v", params)
	}
	if params.System[0].Text != "system" || params.Messages[1].Content[0].OfText.Text != "assistant" || params.MaxTokens != 100 {
		t.Fatalf("lost request fields: %#v", params)
	}
	if _, err := adapter.parameters(&domain.ModelRequest{Messages: []domain.Message{{Role: "tool", Content: "no"}}}); err == nil {
		t.Fatal("unsupported role was accepted")
	}
}

func TestFromMessage(t *testing.T) {
	var message sdk.Message
	if err := json.Unmarshal([]byte(`{"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":6,"output_tokens":4,"cache_read_input_tokens":2,"cache_creation_input_tokens":1,"output_tokens_details":{"thinking_tokens":3}}}`), &message); err != nil {
		t.Fatal(err)
	}
	response, err := fromMessage(&message)
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "hello" || response.FinishReason != "stop" || response.Usage.TotalTokens != 13 || response.Usage.InputTokens != 9 || response.Usage.CachedTokens != 2 || response.Usage.CacheWriteTokens != 1 || response.Usage.ReasoningTokens != 3 || response.Usage.Incomplete {
		t.Fatalf("unexpected normalized response: %#v", response)
	}
	if _, err := fromMessage(nil); err == nil {
		t.Fatal("nil response accepted")
	}
	if _, err := fromMessage(&sdk.Message{}); err == nil {
		t.Fatal("empty response accepted")
	}
	if !normalizeUsage(sdk.Usage{}).Incomplete {
		t.Fatal("absent usage was treated as complete")
	}
}

func TestMessageStreamRecv(t *testing.T) {
	stream := messageStreamForTest(`
event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":0,"cache_read_input_tokens":2}}}

event: content_block_delta
data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"delta"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`)
	defer stream.Close()
	event, err := stream.Recv()
	if err != nil || event.Kind != domain.StreamEventDelta || event.Delta != "delta" {
		t.Fatalf("delta=%#v err=%v", event, err)
	}
	event, err = stream.Recv()
	if err != nil || event.Kind != domain.StreamEventUsage || event.Usage.InputTokens != 7 || event.Usage.CachedTokens != 2 || event.Usage.TotalTokens != 9 || event.Usage.Incomplete {
		t.Fatalf("usage=%#v err=%v", event, err)
	}
	event, err = stream.Recv()
	if err != nil || event.Kind != domain.StreamEventDone || event.FinishReason != "stop" {
		t.Fatalf("done=%#v err=%v", event, err)
	}
	if _, err = stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestMessageStreamRequiresMessageStop(t *testing.T) {
	stream := messageStreamForTest("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}\n\n")
	defer stream.Close()
	if _, err := stream.Recv(); err == nil || core.NormalizeError(err).Code != "upstream_stream_incomplete" {
		t.Fatalf("truncated stream error=%v", err)
	}
}

func TestMergeUsagePreservesExplicitZero(t *testing.T) {
	var usage sdk.Usage
	var delta sdk.MessageDeltaUsage
	if err := json.Unmarshal([]byte(`{"input_tokens":5,"output_tokens":1,"cache_read_input_tokens":4}`), &usage); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"output_tokens":2,"cache_read_input_tokens":0}`), &delta); err != nil {
		t.Fatal(err)
	}
	mergeUsage(&usage, delta)
	if usage.InputTokens != 5 || usage.OutputTokens != 2 || usage.CacheReadInputTokens != 0 {
		t.Fatalf("incorrect cumulative usage: %#v", usage)
	}
}

func messageStreamForTest(events string) *messageStream {
	response := &http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(events))}
	reader := ssestream.NewStream[sdk.MessageStreamEventUnion](ssestream.NewDecoder(response), nil)
	return &messageStream{reader: reader}
}
