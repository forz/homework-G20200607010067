package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/homework-G20200607010067/week1/internal/config"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

func TestHandlerStream(t *testing.T) {
	t.Run("flushes each event before receiving the next", func(t *testing.T) {
		stream := newSSETestStream()
		stream.events <- sseTestResult{event: &domain.StreamEvent{Kind: domain.StreamEventDelta, Delta: "hello\n世界"}}
		url, client := startSSETestServer(t, &sseTestBackend{stream: stream})
		response := requestSSE(t, client, url)
		if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("response status=%d content-type=%q", response.StatusCode, response.Header.Get("Content-Type"))
		}
		if response.Header.Get("Cache-Control") != "no-cache, no-transform" || response.Header.Get("X-Accel-Buffering") != "no" {
			t.Fatalf("stream buffering headers=%v", response.Header)
		}
		if len(response.TransferEncoding) != 1 || response.TransferEncoding[0] != "chunked" {
			t.Fatalf("transfer encoding=%v, want chunked", response.TransferEncoding)
		}
		reader := bufio.NewReader(response.Body)
		first := readSSEChunk(t, reader)
		if first.ID != "chatcmpl-"+response.Header.Get(RequestIDHeader) || first.Model != "deepseek-v4-pro" || first.Object != "chat.completion.chunk" {
			t.Fatalf("first chunk metadata=%+v", first)
		}
		if len(first.Choices) != 1 || first.Choices[0].Delta.Content != "hello\n世界" || first.Choices[0].FinishReason != nil {
			t.Fatalf("first chunk choices=%+v", first.Choices)
		}
		// The upstream cannot produce another event until the client has read the first.
		stream.events <- sseTestResult{}
		stream.events <- sseTestResult{event: &domain.StreamEvent{Kind: domain.StreamEventUsage, Usage: &domain.TokenUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}}}
		stream.events <- sseTestResult{event: &domain.StreamEvent{Kind: domain.StreamEventDone, FinishReason: "stop"}}
		usage := readSSEChunk(t, reader)
		if len(usage.Choices) != 0 || usage.Usage == nil || usage.Usage.TotalTokens != 5 {
			t.Fatalf("usage chunk=%+v", usage)
		}
		finish := readSSEChunk(t, reader)
		if len(finish.Choices) != 1 || finish.Choices[0].FinishReason == nil || *finish.Choices[0].FinishReason != "stop" {
			t.Fatalf("finish chunk=%+v", finish)
		}
		assertSSEDone(t, reader)
		assertSSEStreamClosed(t, stream)
	})

	for _, stage := range []string{"opening upstream", "receiving first event"} {
		t.Run("HTTP error when "+stage, func(t *testing.T) {
			upstreamErr := core.NewError(http.StatusServiceUnavailable, core.ErrorTypeUpstream, "upstream_unavailable", "upstream unavailable")
			backend := &sseTestBackend{}
			if stage == "opening upstream" {
				backend.err = upstreamErr
			} else {
				backend.stream = newSSETestStream()
				backend.stream.events <- sseTestResult{err: upstreamErr}
			}
			url, client := startSSETestServer(t, backend)
			response := requestSSE(t, client, url)
			if response.StatusCode != http.StatusServiceUnavailable || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
				t.Fatalf("pre-stream response status=%d content-type=%q", response.StatusCode, response.Header.Get("Content-Type"))
			}
			var envelope ErrorEnvelope
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatalf("decode HTTP error: %v", err)
			}
			if envelope.Error.Code != "upstream_unavailable" || envelope.Error.RequestID != response.Header.Get(RequestIDHeader) {
				t.Fatalf("HTTP error envelope=%+v", envelope)
			}
			if backend.stream != nil {
				assertSSEStreamClosed(t, backend.stream)
			}
		})
	}

	for _, scenario := range []struct {
		name string
		next sseTestResult
		code string
	}{
		{name: "receive error", next: sseTestResult{err: core.NewError(http.StatusBadGateway, core.ErrorTypeUpstream, "upstream_failed", "upstream failed")}, code: "upstream_failed"},
		{name: "unsupported event", next: sseTestResult{event: &domain.StreamEvent{Kind: "unsupported"}}, code: "internal_error"},
	} {
		t.Run("SSE error after first event for "+scenario.name, func(t *testing.T) {
			stream := newSSETestStream()
			stream.events <- sseTestResult{event: &domain.StreamEvent{Kind: domain.StreamEventDelta, Delta: "partial"}}
			stream.events <- scenario.next
			url, client := startSSETestServer(t, &sseTestBackend{stream: stream})
			response := requestSSE(t, client, url)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("stream status=%d, want 200", response.StatusCode)
			}
			reader := bufio.NewReader(response.Body)
			_ = readSSEChunk(t, reader)
			var envelope ErrorEnvelope
			if err := json.Unmarshal([]byte(readSSEData(t, reader)), &envelope); err != nil {
				t.Fatalf("decode SSE error: %v", err)
			}
			if envelope.Error.Code != scenario.code || envelope.Error.RequestID != response.Header.Get(RequestIDHeader) {
				t.Fatalf("SSE error envelope=%+v, want code=%q", envelope, scenario.code)
			}
			assertSSEDone(t, reader)
			assertSSEStreamClosed(t, stream)
		})
	}

	t.Run("client disconnect cancels and closes upstream", func(t *testing.T) {
		stream := newSSETestStream()
		stream.events <- sseTestResult{event: &domain.StreamEvent{Kind: domain.StreamEventDelta, Delta: "first"}}
		url, client := startSSETestServer(t, &sseTestBackend{stream: stream})
		response := requestSSE(t, client, url)
		_ = readSSEChunk(t, bufio.NewReader(response.Body))
		awaitSSESignal(t, stream.waiting, "upstream waiting for second event")
		if err := response.Body.Close(); err != nil {
			t.Fatalf("close client response: %v", err)
		}
		awaitSSESignal(t, stream.cancelled, "upstream context cancellation")
		assertSSEStreamClosed(t, stream)
	})
}

func TestNewServer(t *testing.T) {
	h := NewServer(&config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:0"}, Limits: testLimits(),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), Handlers{}, nil)
	if !h.GetOptions().SenseClientDisconnection {
		t.Fatal("server must propagate client disconnects to streaming upstream contexts")
	}
}

type sseTestResult struct {
	event *domain.StreamEvent
	err   error
}

type sseTestStream struct {
	ctx       context.Context
	events    chan sseTestResult
	waiting   chan struct{}
	cancelled chan struct{}
	closed    chan struct{}
	stop      chan struct{}
	receives  int
	closes    atomic.Int32
	closeOnce sync.Once
}

func newSSETestStream() *sseTestStream {
	return &sseTestStream{
		events: make(chan sseTestResult, 4), waiting: make(chan struct{}),
		cancelled: make(chan struct{}), closed: make(chan struct{}), stop: make(chan struct{}),
	}
}

func (s *sseTestStream) Recv() (*domain.StreamEvent, error) {
	s.receives++
	if s.receives == 2 {
		close(s.waiting)
	}
	select {
	case result := <-s.events:
		return result.event, result.err
	case <-s.ctx.Done():
		close(s.cancelled)
		return nil, s.ctx.Err()
	case <-s.stop:
		return nil, io.EOF
	}
}

func (s *sseTestStream) Close() error {
	s.closes.Add(1)
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

type sseTestBackend struct {
	stream *sseTestStream
	err    error
}

func (b *sseTestBackend) Generate(context.Context, *domain.ModelRequest) (*domain.ModelResponse, error) {
	return nil, errors.New("unexpected non-streaming generation")
}

func (b *sseTestBackend) Stream(ctx context.Context, _ *domain.ModelRequest) (domain.ModelStream, error) {
	if b.err != nil {
		return nil, b.err
	}
	b.stream.ctx = ctx
	return b.stream, nil
}

func (*sseTestBackend) Models() []string { return []string{"deepseek-v4-pro"} }

func startSSETestServer(t *testing.T, backend *sseTestBackend) (string, *http.Client) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	h := server.New(server.WithListener(listener), server.WithSenseClientDisconnection(true))
	h.Use(RequestID(), Authenticate("test-key"))
	h.POST("/v1/chat/completions", NewHandler(backend, testLimits()).ChatCompletions)
	runDone := make(chan error, 1)
	go func() { runDone <- h.Run() }()
	transport := &http.Transport{DisableKeepAlives: true}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	t.Cleanup(func() {
		if backend.stream != nil {
			close(backend.stream.stop)
		}
		transport.CloseIdleConnections()
		_ = h.Close()
		_ = listener.Close()
		select {
		case err := <-runDone:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Errorf("Hertz server: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Hertz server did not stop")
		}
	})
	return "http://" + listener.Addr().String() + "/v1/chat/completions", client
}

func requestSSE(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer test-key")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("receive SSE headers while upstream is still open: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func readSSEData(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read SSE data: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("SSE event must contain only a data field, got %q", line)
	}
	separator, err := reader.ReadString('\n')
	if err != nil || separator != "\n" {
		t.Fatalf("SSE frame delimiter=%q err=%v", separator, err)
	}
	return strings.TrimSuffix(strings.TrimPrefix(line, "data: "), "\n")
}

func readSSEChunk(t *testing.T, reader *bufio.Reader) chatCompletionChunk {
	t.Helper()
	var chunk chatCompletionChunk
	if err := json.Unmarshal([]byte(readSSEData(t, reader)), &chunk); err != nil {
		t.Fatalf("decode chunk: %v", err)
	}
	return chunk
}

func assertSSEDone(t *testing.T, reader *bufio.Reader) {
	t.Helper()
	if data := readSSEData(t, reader); data != "[DONE]" {
		t.Fatalf("terminal data=%q, want [DONE]", data)
	}
	if trailing, err := io.ReadAll(reader); err != nil || len(trailing) != 0 {
		t.Fatalf("stream must end after exactly one [DONE]: trailing=%q err=%v", trailing, err)
	}
}

func assertSSEStreamClosed(t *testing.T, stream *sseTestStream) {
	t.Helper()
	awaitSSESignal(t, stream.closed, "upstream stream close")
	if got := stream.closes.Load(); got != 1 {
		t.Fatalf("stream closed %d times, want once", got)
	}
}

func awaitSSESignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
