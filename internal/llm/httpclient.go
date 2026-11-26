package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	maxRetries    = 2
	maxRetryWait  = 10 * time.Second
	maxReplyBytes = 32 << 20
)

// APIError is a non-2xx response from a provider. Message never contains the API key.
type APIError struct {
	Provider string
	Status   int
	Message  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s API error %d: %s", e.Provider, e.Status, e.Message)
}

// retryable reports whether a status is worth retrying: rate limits, server
// errors, and Anthropic's 529 "overloaded".
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// postJSON sends body to url and returns the response body and status. It
// retries connection errors and retryable statuses up to maxRetries times,
// waiting Retry-After (capped) or a short backoff. Each attempt is bounded by
// the client's timeout. Retries never exceed the caller's context.
func postJSON(ctx context.Context, c *http.Client, url string, headers map[string]string, body []byte) ([]byte, int, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
			if ctx.Err() != nil || attempt == maxRetries {
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
