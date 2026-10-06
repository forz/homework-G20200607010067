package adapters

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusErrorTransport(t *testing.T) {
	t.Run("successful response is preserved", func(t *testing.T) {
		transport := statusErrorTransport{base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       http.NoBody,
				Header:     make(http.Header),
			}, nil
		})}
		response, err := transport.RoundTrip(httptest.NewRequest(http.MethodGet, "http://example.com", nil))
		if err != nil || response == nil || response.StatusCode != http.StatusOK {
			t.Fatalf("RoundTrip() response=%v err=%v, want status 200", response, err)
		}
	})

	t.Run("error response is reduced to status only", func(t *testing.T) {
		body := &trackingBody{Reader: strings.NewReader("sensitive-provider-detail")}
		transport := statusErrorTransport{base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Body:       body,
				Header:     make(http.Header),
			}, nil
		})}
		response, err := transport.RoundTrip(httptest.NewRequest(http.MethodGet, "http://example.com", nil))
		var statusErr *HTTPStatusError
		if response != nil || !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("RoundTrip() response=%v err=%v, want status-only 429 error", response, err)
		}
		if !body.closed {
			t.Fatal("upstream error response body was not closed")
		}
		if strings.Contains(err.Error(), "sensitive-provider-detail") {
			t.Fatalf("public transport error leaked response body: %q", err)
		}
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type trackingBody struct {
	*strings.Reader
	closed bool
}

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}
