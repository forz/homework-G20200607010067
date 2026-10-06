package openai

import (
	"errors"
	"io"
	"testing"

	"github.com/cloudwego/eino/schema"
	schemaopenai "github.com/cloudwego/eino/schema/openai"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

func TestToAgenticMessages(t *testing.T) {
	messages, err := toAgenticMessages([]domain.Message{
		{Role: domain.RoleSystem, Content: "system"},
		{Role: domain.RoleDeveloper, Content: "developer"},
		{Role: domain.RoleUser, Content: "user"},
		{Role: domain.RoleAssistant, Content: "assistant"},
	})
	if err != nil {
		t.Fatalf("toAgenticMessages() error=%v", err)
	}
	if len(messages) != 4 || messages[0].Role != schema.AgenticRoleTypeSystem || messages[1].Role != schema.AgenticRoleTypeSystem || messages[2].Role != schema.AgenticRoleTypeUser || messages[3].Role != schema.AgenticRoleTypeAssistant {
		t.Fatalf("unexpected role mapping: %#v", messages)
	}
	if got := agenticText(messages[3]); got != "assistant" {
		t.Fatalf("assistant text=%q, want assistant", got)
	}
	if _, err := toAgenticMessages([]domain.Message{{Role: "tool", Content: "no"}}); err == nil {
		t.Fatal("unsupported role was accepted")
	}
}

func TestFromAgenticMessage(t *testing.T) {
	message := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			nil,
			schema.NewContentBlock(&schema.AssistantGenText{Text: "hello "}),
			schema.NewContentBlock(&schema.AssistantGenText{Text: "world"}),
		},
		ResponseMeta: &schema.AgenticResponseMeta{TokenUsage: &schema.TokenUsage{
			PromptTokens: 4, CompletionTokens: 3,
		}},
	}
	response, err := fromAgenticMessage(message)
	if err != nil {
		t.Fatalf("fromAgenticMessage() error=%v", err)
	}
	if response.Content != "hello world" || response.Usage.InputTokens != 4 || response.Usage.OutputTokens != 3 || response.Usage.TotalTokens != 7 {
		t.Fatalf("unexpected normalized response: %#v", response)
	}
	if _, err := fromAgenticMessage(nil); err == nil {
		t.Fatal("nil response was accepted")
	}
	if _, err := fromAgenticMessage(&schema.AgenticMessage{}); err == nil {
		t.Fatal("empty assistant response was accepted")
	}
	if usage := normalizeAgenticUsage(nil); !usage.Incomplete {
		t.Fatalf("missing usage=%#v, want incomplete", usage)
	}
}

func TestResponseStreamRecv(t *testing.T) {
	usage := &schema.TokenUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}
	reader := schema.StreamReaderFromArray([]*schema.AgenticMessage{{
		ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: "delta"})},
		ResponseMeta: &schema.AgenticResponseMeta{TokenUsage: usage,
			OpenAIExtension: &schemaopenai.ResponseMetaExtension{Status: "completed"}},
	}})
	stream := &responseStream{reader: reader, finishReason: "stop"}
	defer stream.Close()

	event, err := stream.Recv()
	if err != nil || event.Kind != domain.StreamEventDelta || event.Delta != "delta" {
		t.Fatalf("first Recv() event=%#v err=%v", event, err)
	}
	event, err = stream.Recv()
	if err != nil || event.Kind != domain.StreamEventUsage || event.Usage == nil || event.Usage.TotalTokens != 7 {
		t.Fatalf("usage Recv() event=%#v err=%v", event, err)
	}
	event, err = stream.Recv()
	if err != nil || event.Kind != domain.StreamEventDone || event.FinishReason != "stop" {
		t.Fatalf("done Recv() event=%#v err=%v", event, err)
	}
	if event, err = stream.Recv(); event != nil || !errors.Is(err, io.EOF) {
		t.Fatalf("terminal Recv() event=%#v err=%v, want EOF", event, err)
	}
}
