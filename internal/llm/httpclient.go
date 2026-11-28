package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"sync/atomic"
	"time"
)

const (
	maxRetries    = 2
	maxRetryWait  = 10 * time.Second
	maxReplyBytes = 32 << 20
)

// ErrContextLength means the conversation no longer fits the model's context window.
var ErrContextLength = errors.New("conversation is too long for the model's context window")

// APIError is a non-2xx response from a provider. Message never contains the API key.
type APIError struct {
	Provider string
	Status   int
	Message  string
	// ContextTooLong is set when the provider says the input exceeds the context window.
	ContextTooLong bool
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s API error %d: %s", e.Provider, e.Status, e.Message)
}

// Is makes errors.Is(err, ErrContextLength) true for context-window errors.
func (e *APIError) Is(target error) bool { return target == ErrContextLength && e.ContextTooLong }

// retryable reports whether a status is worth retrying: rate limits, server
// errors, and Anthropic's 529 "overloaded".
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// postJSON sends body to url and returns the response body and status. It
// retries retryable statuses, and errors from before the request was sent
// (dial, TLS, connection refused), up to maxRetries times, waiting Retry-After
// (capped) or a short backoff. A timeout or failure after the request was sent
// is not retried: the provider may already have generated, and billed, the reply.
// Each attempt is bounded by the client's timeout and by ctx.
func postJSON(ctx context.Context, c *http.Client, url string, headers map[string]string, body []byte) ([]byte, int, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		var sent atomic.Bool
		trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				sent.Store(true)
			}
		}}
		req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		wait := time.Duration(1<<attempt) * 500 * time.Millisecond
		res, err := c.Do(req)
		if err == nil {
			raw, rerr := io.ReadAll(io.LimitReader(res.Body, maxReplyBytes))
			res.Body.Close()
			if rerr != nil {
				err = fmt.Errorf("read response: %w", rerr)
			} else if !retryable(res.StatusCode) || attempt == maxRetries {
				return raw, res.StatusCode, nil
			} else {
				if s, perr := strconv.Atoi(res.Header.Get("Retry-After")); perr == nil && s >= 0 {
					wait = time.Duration(s) * time.Second
				}
				lastErr = fmt.Errorf("status %d", res.StatusCode)
			}
		}
		if err != nil {
			if ctx.Err() != nil || attempt == maxRetries || sent.Load() {
				return nil, 0, err
			}
			lastErr = err
		}
		if wait > maxRetryWait {
			wait = maxRetryWait
		}
		select {
		case <-ctx.Done():
			return nil, 0, errors.Join(ctx.Err(), lastErr)
		case <-time.After(wait):
		}
	}
}
