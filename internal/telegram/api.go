package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type apiError struct {
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram: API %d: %s", e.Code, e.Description)
}

func (b *Bot) call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return errors.New("telegram: cannot encode request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/bot"+b.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return errors.New("telegram: invalid API endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Remove the request URL, which contains the bot token, but keep the cause.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("telegram: network request failed: %s", strings.ReplaceAll(err.Error(), b.token, "[redacted]"))
	}
	defer resp.Body.Close()
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&envelope); err != nil {
		if resp.StatusCode >= 400 {
			return &apiError{Code: resp.StatusCode, Description: http.StatusText(resp.StatusCode)}
		}
		return errors.New("telegram: invalid JSON response")
	}
	if !envelope.OK || resp.StatusCode >= 400 {
		code := envelope.ErrorCode
		if code == 0 {
			code = resp.StatusCode
			if code < 400 {
				code = http.StatusBadRequest
			}
		}
		return &apiError{
			Code: code, Description: strings.ReplaceAll(envelope.Description, b.token, "[redacted]"),
			RetryAfter: time.Duration(envelope.Parameters.RetryAfter) * time.Second,
		}
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("telegram: invalid %s result", method)
		}
	}
	return nil
}

func (b *Bot) callWithRetry(ctx context.Context, method string, params, out any) error {
	delay := time.Second
	for attempt := range 3 {
		err := b.call(ctx, method, params, out)
		if err == nil || ctx.Err() != nil || !retryable(err) || attempt == 2 {
			return err
		}
		b.log(err)
		if err := wait(ctx, retryDelay(err, delay)); err != nil {
			return err
		}
		delay *= 2
	}
	return errors.New("telegram: retry limit reached")
}

func retryable(err error) bool {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.Code == http.StatusTooManyRequests || apiErr.Code >= 500
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func retryDelay(err error, fallback time.Duration) time.Duration {
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		return max(fallback, apiErr.RetryAfter)
	}
	return fallback
}
