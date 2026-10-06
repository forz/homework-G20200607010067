package services

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/homework-G20200607010067/week1/internal/adapters"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

// Stream routes a request and wraps it with cancellation, close-once, and
// first-non-empty-text timing semantics.
func (s *GatewayService) Stream(ctx context.Context, request *domain.ModelRequest) (domain.ModelStream, error) {
	started := time.Now()
	usageEvent := newUsageEvent(request, true, started)
	fail := func(err error) (domain.ModelStream, error) {
		finishUsageEvent(&usageEvent, started, nil, err)
		s.persistUsage(usageEvent)
		return nil, err
	}
	route, err := s.router.Resolve(request.ModelAlias)
	if err != nil {
		return fail(err)
	}
	usageEvent.Protocol = route.Adapter.Protocol()
	usageEvent.UpstreamModel = route.UpstreamModel
	prepared, err := s.prepareRequest(ctx, request)
	if err != nil {
		return fail(err)
	}
	if prepared.PromptMetadata != nil {
		usageEvent.PromptID = prepared.PromptMetadata.ID
		version := prepared.PromptMetadata.Version
		usageEvent.PromptVersion = &version
	}
	upstreamRequest := *prepared
	upstreamRequest.UpstreamModel = route.UpstreamModel
	validator, err := compileStructured(prepared.ResponseFormat, s.maxSchemaBytes, s.maxSchemaDepth)
	if err != nil {
		return fail(err)
	}
	if s.limiter != nil && !s.limiter.Allow(request.ModelAlias) {
		return fail(core.NewError(
			http.StatusTooManyRequests, core.ErrorTypeRateLimit, "model_rate_limited",
			"model request rate limit exceeded",
		).WithParam("model").WithRetryable(true))
	}
	streamContext, cancel := context.WithCancel(ctx)
	factory := func() (domain.ModelStream, error) {
		return route.Adapter.Stream(streamContext, &upstreamRequest)
	}
	inner, retries, err := openStreamWithRetry(streamContext, s.retryPolicy, factory)
	if err != nil {
		cancel()
		usageEvent.TransportRetries = retries
		return fail(adapters.NormalizeUpstreamError(err))
	}
	return &managedStream{
		inner: inner, cancel: cancel, started: started,
		validator: validator, maxBufferBytes: s.maxStreamBufferBytes,
		ctx: streamContext, factory: factory, retryPolicy: s.retryPolicy, retries: retries,
		onFinish: func(err error, usage domain.TokenUsage, firstToken *time.Duration, retries int) {
			usageEvent.Usage = usage
			usageEvent.TransportRetries = retries
			if firstToken != nil {
				milliseconds := float64(firstToken.Microseconds()) / 1000
				usageEvent.FirstTokenMS = &milliseconds
			}
			finishUsageEvent(&usageEvent, started, nil, err)
			usageEvent.Usage = usage
			s.persistUsage(usageEvent)
		},
	}, nil
}

type managedStream struct {
	inner          domain.ModelStream
	cancel         context.CancelFunc
	started        time.Time
	firstToken     *time.Duration
	closeOnce      sync.Once
	validator      *StructuredValidator
	buffer         bytes.Buffer
	maxBufferBytes int
	usage          domain.TokenUsage
	ctx            context.Context
	factory        func() (domain.ModelStream, error)
	retryPolicy    core.RetryPolicy
	retries        int
	onFinish       func(error, domain.TokenUsage, *time.Duration, int)
	finishOnce     sync.Once
	done           bool
}

func (s *managedStream) Recv() (*domain.StreamEvent, error) {
	var event *domain.StreamEvent
	for {
		var err error
		event, err = s.inner.Recv()
		if errors.Is(err, io.EOF) {
			if !s.done {
				err = core.NewError(http.StatusBadGateway, core.ErrorTypeUpstream, "upstream_stream_incomplete", "upstream stream ended without a terminal event")
				s.usage.Incomplete = true
				s.finish(err)
				return nil, err
			}
			s.finish(nil)
			return event, err
		}
		if err == nil {
			break
		}
		if s.firstToken == nil {
			if restarted, restartErr := s.restartBeforeText(err); restarted {
				continue
			} else if restartErr != nil {
				err = restartErr
			}
		}
		normalized := adapters.NormalizeUpstreamError(err)
		s.usage.Incomplete = true
		s.finish(normalized)
		return nil, normalized
	}
	if event == nil {
		return nil, nil
	}
	// Adapters may attach the latest known usage to a text chunk, so even a
	// later interruption can persist the observed tokens without inventing totals.
	if event.Usage != nil {
		s.usage = *event.Usage
	}
	if event.Kind == domain.StreamEventDelta && event.Delta != "" {
		if s.firstToken == nil {
			latency := time.Since(s.started)
			s.firstToken = &latency
		}
		if s.validator != nil {
			if s.buffer.Len()+len(event.Delta) > s.maxBufferBytes {
				err := core.NewError(
					http.StatusUnprocessableEntity, core.ErrorTypeUpstream, "structured_output_too_large",
					"streamed structured output exceeded the validation limit",
				)
				s.usage.Incomplete = true
				s.finish(err)
				return nil, err
			}
			s.buffer.WriteString(event.Delta)
		}
	}
	if event.Kind == domain.StreamEventUsage && event.Usage != nil {
		s.usage = *event.Usage
	}
	if event.Kind == domain.StreamEventDone && s.validator != nil {
		if validationErr := s.validator.ValidateText(s.buffer.String()); validationErr != nil {
			s.finish(validationErr)
			return nil, validationErr
		}
	}
	if event.Kind == domain.StreamEventDone {
		s.done = true
		s.finish(nil)
	}
	return event, nil
}

func (s *managedStream) restartBeforeText(cause error) (bool, error) {
	currentErr := cause
	for adapters.IsRetryable(currentErr) && s.retries < s.retryPolicy.MaxRetries {
		_ = s.inner.Close()
		if err := s.retryPolicy.Wait(s.ctx, s.retries); err != nil {
			return false, err
		}
		s.retries++
		restarted, err := s.factory()
		if err == nil {
			s.inner = restarted
			return true, nil
		}
		currentErr = err
	}
	return false, currentErr
}

func (s *managedStream) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		s.cancel()
		closeErr = s.inner.Close()
		s.finish(context.Canceled)
	})
	return closeErr
}

func (s *managedStream) finish(err error) {
	s.finishOnce.Do(func() {
		if s.onFinish != nil {
			s.onFinish(err, s.usage, s.firstTokenLatency(), s.retries)
		}
	})
}

func openStreamWithRetry(
	ctx context.Context,
	policy core.RetryPolicy,
	factory func() (domain.ModelStream, error),
) (domain.ModelStream, int, error) {
	stream, err := factory()
	retries := 0
	for err != nil && adapters.IsRetryable(err) && retries < policy.MaxRetries {
		if waitErr := policy.Wait(ctx, retries); waitErr != nil {
			return nil, retries, waitErr
		}
		retries++
		stream, err = factory()
	}
	return stream, retries, err
}

func (s *managedStream) firstTokenLatency() *time.Duration {
	if s.firstToken == nil {
		return nil
	}
	copyOf := *s.firstToken
	return &copyOf
}
