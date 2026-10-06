package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

func TestManagedStreamRecordsStructuredOverflow(t *testing.T) {
	validator, err := compileStructured(&domain.JSONSchemaFormat{
		Name: "value", Strict: true, Schema: json.RawMessage(`{"type":"string"}`),
	}, 1024, 8)
	if err != nil {
		t.Fatalf("compile validator: %v", err)
	}
	inner := &fakeStream{results: []streamResult{{event: &domain.StreamEvent{Kind: domain.StreamEventDelta, Delta: `"too long"`}}}}
	var terminal error
	stream := &managedStream{
		inner: inner, cancel: func() {}, started: time.Now(), validator: validator,
		maxBufferBytes: 2, ctx: context.Background(),
		onFinish: func(err error, _ domain.TokenUsage, _ *time.Duration, _ int) { terminal = err },
	}
	if _, err := stream.Recv(); err == nil || core.NormalizeError(err).Code != "structured_output_too_large" {
		t.Fatalf("Recv() error=%v, want structured_output_too_large", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close() error=%v", err)
	}
	if got := core.NormalizeError(terminal).Code; got != "structured_output_too_large" {
		t.Fatalf("terminal usage error code=%q, want structured_output_too_large", got)
	}
}
