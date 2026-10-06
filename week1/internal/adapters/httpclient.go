package adapters

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// NewHTTPClient creates the shared bounded client used by provider adapters.
// The caller owns no background goroutines; idle connections may be closed on shutdown.
func NewHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	transport.DialContext = (&net.Dialer{
		Timeout:   min(timeout, 10*time.Second),
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 20
	transport.IdleConnTimeout = 90 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = timeout
	return &http.Client{Transport: statusErrorTransport{base: transport}, Timeout: timeout}
}

// CloseIdleConnections releases pooled connections held by a shared client.
func CloseIdleConnections(client *http.Client) {
	if client != nil {
		client.CloseIdleConnections()
	}
}

// HTTPStatusError preserves only an upstream status code and discards response
// bodies so provider errors cannot leak through public error messages or logs.
type HTTPStatusError struct{ StatusCode int }

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("upstream returned HTTP status %d", e.StatusCode)
}

type statusErrorTransport struct{ base http.RoundTripper }

func (t statusErrorTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusBadRequest {
		return response, nil
	}
	_, _ = io.CopyN(io.Discard, response.Body, 4096)
	_ = response.Body.Close()
	return nil, &HTTPStatusError{StatusCode: response.StatusCode}
}
