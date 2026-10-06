package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/sse"
	"github.com/homework-G20200607010067/week1/internal/config"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

// ChatBackend is the transport-independent model service consumed by handlers.
type ChatBackend interface {
	Generate(context.Context, *domain.ModelRequest) (*domain.ModelResponse, error)
	Stream(context.Context, *domain.ModelRequest) (domain.ModelStream, error)
	Models() []string
}

// Handler owns HTTP DTO decoding and response encoding only.
type Handler struct {
	backend ChatBackend
	limits  config.LimitsConfig
	now     func() time.Time
}

// NewHandler creates the model-facing Hertz handler set.
func NewHandler(backend ChatBackend, limits config.LimitsConfig) *Handler {
	return &Handler{backend: backend, limits: limits, now: time.Now}
}

// ChatCompletions serves the Chat Completions-compatible endpoint, including SSE.
func (h *Handler) ChatCompletions(ctx context.Context, request *app.RequestContext) {
	var input ChatCompletionRequest
	if err := decodeStrictJSON(request.Request.Body(), &input); err != nil {
		WriteError(request, core.WrapError(
			http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid_json", "request body must be valid JSON", err,
		))
		return
	}
	if validationErr := ValidateChatRequest(&input, h.limits); validationErr != nil {
		WriteError(request, validationErr)
		return
	}
	if input.Stream {
		h.stream(ctx, request, &input)
		return
	}
	requestID := RequestIDFrom(request)
	result, err := h.backend.Generate(ctx, input.ToModelRequest(requestID))
	if err != nil {
		WriteError(request, err)
		return
	}
	request.JSON(http.StatusOK, ChatCompletion{
		ID: "chatcmpl-" + requestID, Object: "chat.completion", Created: h.now().Unix(), Model: input.Model,
		Choices: []CompletionChoice{{
			Index: 0, Message: AssistantMessage{Role: "assistant", Content: result.Content},
			FinishReason: result.FinishReason,
		}},
		Usage: toUsage(result.Usage),
	})
}

func (h *Handler) stream(ctx context.Context, request *app.RequestContext, input *ChatCompletionRequest) {
	requestID := RequestIDFrom(request)
	stream, err := h.backend.Stream(ctx, input.ToModelRequest(requestID))
	if err != nil {
		WriteError(request, err)
		return
	}
	defer stream.Close()
	id := "chatcmpl-" + requestID
	created := h.now().Unix()
	var writer *sse.Writer
	for {
		event, recvErr := stream.Recv()
		if recvErr == nil && event == nil {
			continue
		}
		var payload []byte
		if recvErr == nil {
			payload, recvErr = encodeStreamEvent(id, input.Model, created, event)
		}
		if recvErr != nil {
			if writer == nil {
				WriteError(request, recvErr)
			} else if !errors.Is(recvErr, io.EOF) {
				writeStreamError(writer, requestID, recvErr)
			}
			return
		}
		if writer == nil {
			// Delay committing SSE headers until the first event is ready so
			// startup failures can still use the normal HTTP error response.
			writer = sse.NewWriter(request)
			defer writer.Close()
			request.Response.Header.Set("Cache-Control", "no-cache, no-transform")
			request.Response.Header.Set("Connection", "keep-alive")
			request.Response.Header.Set("X-Accel-Buffering", "no")
			request.SetStatusCode(http.StatusOK)
		}
		// WriteEvent frames and flushes each data-only Chat Completions event.
		if err := writer.WriteEvent("", "", payload); err != nil {
			return
		}
		if event.Kind == domain.StreamEventDone {
			_ = writer.WriteEvent("", "", []byte("[DONE]"))
			return
		}
	}
}

// Models returns all configured public aliases without exposing upstream IDs.
func (h *Handler) Models(_ context.Context, request *app.RequestContext) {
	aliases := h.backend.Models()
	data := make([]ModelObject, 0, len(aliases))
	for _, alias := range aliases {
		data = append(data, ModelObject{ID: alias, Object: "model", Created: 0, OwnedBy: "llm-gateway"})
	}
	request.JSON(http.StatusOK, ModelList{Object: "list", Data: data})
}

func decodeStrictJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
