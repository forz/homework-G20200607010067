package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/homework-G20200607010067/week1/internal/config"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

func TestResponseFormatIsolation(t *testing.T) {
	formats := map[string]*domain.JSONSchemaFormat{"plain": nil}
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("schema_%d", i)
		formats[name] = &domain.JSONSchemaFormat{
			Name: name, Strict: i%2 == 0,
			Schema: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"value":{"type":"string","const":%q}},"required":["value"],"additionalProperties":false}`, name)),
		}
	}

	const responseBody = `{"id":"resp_test","object":"response","status":"completed","model":"test","output":[{"type":"message","id":"msg_test","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model           string          `json:"model"`
			Stream          bool            `json:"stream"`
			Text            json.RawMessage `json:"text"`
			Temperature     float64         `json:"temperature"`
			TopP            float64         `json:"top_p"`
			MaxOutputTokens int             `json:"max_output_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		format, ok := formats[body.Model]
		if !ok {
			t.Errorf("unexpected upstream model %q", body.Model)
		} else if format == nil {
			if len(body.Text) != 0 {
				t.Errorf("plain request inherited text configuration: %s", body.Text)
			}
		} else {
			var got map[string]any
			if err := json.Unmarshal(body.Text, &got); err != nil {
				t.Errorf("decode text configuration: %v", err)
			}
			var schema map[string]any
			if err := json.Unmarshal(format.Schema, &schema); err != nil {
				t.Errorf("decode expected schema: %v", err)
			}
			want := map[string]any{"format": map[string]any{
				"type": "json_schema", "name": format.Name, "strict": format.Strict, "schema": schema,
			}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s text configuration=%s, want %#v", body.Model, body.Text, want)
			}
		}
		if body.Temperature != 0.25 || body.TopP != 0.75 || body.MaxOutputTokens != 64 {
			t.Errorf("per-call generation options were lost: %#v", body)
		}
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"item_id\":\"msg_test\",\"delta\":\"ok\"}\n\n")
			fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", responseBody)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, responseBody)
	}))
	defer server.Close()
	adapter, err := New(context.Background(), config.ModelConfig{
		BaseURL: server.URL, APIKey: "test-key", UpstreamModel: "default-model",
		Timeout: config.Duration{Duration: 5 * time.Second}, MaxTokens: 128,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	invoke := func(t *testing.T, name string, streaming bool) {
		t.Helper()
		temperature, topP, maxTokens := float32(0.25), float32(0.75), 64
		request := &domain.ModelRequest{
			UpstreamModel: name, ResponseFormat: formats[name],
			Messages:    []domain.Message{{Role: domain.RoleUser, Content: "hello"}},
			Temperature: &temperature, TopP: &topP, MaxTokens: &maxTokens,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if !streaming {
			response, err := adapter.Generate(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if response.Content != "ok" {
				t.Errorf("Generate content=%q, want ok", response.Content)
			}
			return
		}
		stream, err := adapter.Stream(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		var content strings.Builder
		var done bool
		for {
			event, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			content.WriteString(event.Delta)
			done = done || event.Kind == domain.StreamEventDone
		}
		if content.String() != "ok" || !done {
			t.Errorf("Stream content=%q done=%v, want ok and done", content.String(), done)
		}
	}

	// Each parallel call uses a distinct schema or no schema on the same model.
	t.Run("concurrent", func(t *testing.T) {
		for name := range formats {
			for _, streaming := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t", name, streaming), func(t *testing.T) {
					t.Parallel()
					invoke(t, name, streaming)
				})
			}
		}
	})
	// A later plain request must not inherit the last structured request's schema.
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("plain_after/stream=%t", streaming), func(t *testing.T) {
			invoke(t, "plain", streaming)
		})
	}
}

func TestInvalidResponseFormatDoesNotCallUpstream(t *testing.T) {
	// An uninitialized adapter makes an accidental upstream call fail immediately.
	adapter := &Adapter{}
	request := &domain.ModelRequest{
		Messages:       []domain.Message{{Role: domain.RoleUser, Content: "hello"}},
		ResponseFormat: &domain.JSONSchemaFormat{Name: "invalid", Schema: json.RawMessage(`{"type":`)},
	}
	if _, err := adapter.Generate(context.Background(), request); err == nil {
		t.Error("Generate accepted malformed schema")
	}
	if _, err := adapter.Stream(context.Background(), request); err == nil {
		t.Error("Stream accepted malformed schema")
	}
}
