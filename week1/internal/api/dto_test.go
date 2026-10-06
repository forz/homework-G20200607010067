package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/homework-G20200607010067/week1/internal/config"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

func TestValidateChatRequest(t *testing.T) {
	limits := testLimits()
	valid := func() *ChatCompletionRequest {
		return &ChatCompletionRequest{
			Model:    "deepseek-v4-pro",
			Messages: []ChatMessage{{Role: "user", Content: "hello"}},
		}
	}
	tests := []struct {
		name string
		edit func(*ChatCompletionRequest)
		code string
	}{
		{name: "valid request", edit: func(*ChatCompletionRequest) {}, code: ""},
		{name: "missing model", edit: func(r *ChatCompletionRequest) { r.Model = " " }, code: "missing_model"},
		{name: "empty messages", edit: func(r *ChatCompletionRequest) { r.Messages = nil }, code: "invalid_messages"},
		{name: "unsupported role", edit: func(r *ChatCompletionRequest) { r.Messages[0].Role = "tool" }, code: "unsupported_role"},
		{name: "oversized content", edit: func(r *ChatCompletionRequest) { r.Messages[0].Content = strings.Repeat("x", limits.MaxMessageBytes+1) }, code: "invalid_message_content"},
		{name: "invalid prompt version", edit: func(r *ChatCompletionRequest) {
			version := 0
			r.PromptRef = &PromptReference{ID: "p", Version: &version}
		}, code: "invalid_prompt_version"},
		{name: "nested prompt variable", edit: func(r *ChatCompletionRequest) {
			r.PromptRef = &PromptReference{ID: "p", Variables: map[string]any{"nested": map[string]any{"x": true}}}
		}, code: "invalid_prompt_variable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid()
			test.edit(request)
			err := ValidateChatRequest(request, limits)
			if test.code == "" && err != nil {
				t.Fatalf("valid request rejected: %v", err)
			}
			if test.code != "" && (err == nil || err.Code != test.code) {
				t.Fatalf("validation error=%#v, want code=%q", err, test.code)
			}
		})
	}
}

func TestValidateResponseFormat(t *testing.T) {
	limits := testLimits()
	validSchema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)
	strict := false
	format := &ResponseFormatRequest{
		Type:       "json_schema",
		JSONSchema: JSONSchemaRequest{Name: "person", Strict: &strict, Schema: validSchema},
	}
	if err := validateResponseFormat(format, limits); err != nil {
		t.Fatalf("valid response format rejected: %v", err)
	}
	request := (&ChatCompletionRequest{
		Model: "deepseek-v4-pro", Messages: []ChatMessage{{Role: "user", Content: "hello"}},
		ResponseFormat: format,
	}).ToModelRequest("req_1")
	if request.ResponseFormat == nil || request.ResponseFormat.Strict || string(request.ResponseFormat.Schema) != string(validSchema) {
		t.Fatalf("ToModelRequest() structured format=%#v", request.ResponseFormat)
	}
	format.JSONSchema.Schema[0] = '['
	if request.ResponseFormat.Schema[0] != '{' {
		t.Fatal("ToModelRequest() did not isolate schema bytes from transport DTO mutation")
	}

	invalid := *format
	invalid.Type = "json_object"
	if err := validateResponseFormat(&invalid, limits); err == nil || err.Code != "unsupported_response_format" {
		t.Fatalf("unsupported format error=%#v", err)
	}
}

func TestDecodeStrictJSON(t *testing.T) {
	var target struct {
		Name string `json:"name"`
	}
	if err := decodeStrictJSON([]byte(`{"name":"alice"}`), &target); err != nil || target.Name != "alice" {
		t.Fatalf("decodeStrictJSON() target=%#v err=%v", target, err)
	}
	if err := decodeStrictJSON([]byte(`{"name":"alice","unknown":true}`), &target); err == nil {
		t.Fatal("unknown field was accepted")
	}
	if err := decodeStrictJSON([]byte(`{"name":"alice"} {"name":"bob"}`), &target); err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
}

func TestEncodeStreamEvent(t *testing.T) {
	usage := domain.TokenUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}
	tests := []struct {
		name  string
		event *domain.StreamEvent
		want  string
	}{
		{name: "delta", event: &domain.StreamEvent{Kind: domain.StreamEventDelta, Delta: "hello"}, want: `"content":"hello"`},
		{name: "usage", event: &domain.StreamEvent{Kind: domain.StreamEventUsage, Usage: &usage}, want: `"total_tokens":5`},
		{name: "done", event: &domain.StreamEvent{Kind: domain.StreamEventDone, FinishReason: "stop"}, want: `"finish_reason":"stop"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := encodeStreamEvent("id", "model", 1, test.event)
			if err != nil || !strings.Contains(string(payload), test.want) {
				t.Fatalf("encodeStreamEvent() payload=%s err=%v, want fragment %s", payload, err, test.want)
			}
		})
	}
	if _, err := encodeStreamEvent("id", "model", 1, &domain.StreamEvent{Kind: "unknown"}); err == nil {
		t.Fatal("unknown stream event kind was accepted")
	}
}

func testLimits() config.LimitsConfig {
	return config.LimitsConfig{
		MaxMessages: 4, MaxMessageBytes: 64, MaxSchemaBytes: 1024,
		MaxSchemaDepth: 8, MaxTemplateBytes: 1024, MaxRenderedBytes: 1024,
		MaxStreamBufferBytes: 1024, MaxQueryLimit: 100, MaxBodyBytes: 4096,
	}
}
