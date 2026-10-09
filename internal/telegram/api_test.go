package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateLimitAndTokenRedaction(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(429)
		io.WriteString(w, `{"ok":false,"error_code":429,"description":"secret-token rate limited","parameters":{"retry_after":3600}}`)
	}))
	defer server.Close()
	bot, err := New("secret-token", []int64{1}, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	bot.baseURL = server.URL
	err = bot.call(context.Background(), "sendMessage", struct{}{}, nil)
	if err == nil || strings.Contains(err.Error(), "secret-token") || retryDelay(err, time.Second) != time.Hour {
		t.Fatalf("rate limit or token redaction: %v", err)
	}
	calls.Store(0)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = bot.callWithRetry(ctx, "sendMessage", struct{}{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
		t.Fatalf("retry_after not respected: calls=%d err=%v", calls.Load(), err)
	}
	bot.baseURL = "http://127.0.0.1:0"
	err = bot.call(context.Background(), "getUpdates", struct{}{}, nil)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("network error leaked token: %v", err)
	}
}

func TestAuthenticationErrorsAreNotRetried(t *testing.T) {
	for _, code := range []int{400, 401, 403, 409} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(code)
				io.WriteString(w, "not JSON")
			}))
			defer server.Close()
			bot, err := New("secret-token", []int64{1}, nil, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			bot.baseURL = server.URL
			if err := bot.callWithRetry(context.Background(), "getUpdates", struct{}{}, nil); err == nil || calls.Load() != 1 {
				t.Fatalf("fatal API error retried: %v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestBotRequiresCredentialsAndAllowlist(t *testing.T) {
	for _, users := range [][]int64{nil, {0}, {-1}} {
		if _, err := New("token", users, nil, io.Discard); err == nil {
			t.Fatal("invalid allowlist accepted")
		}
	}
	if _, err := New("", []int64{1}, nil, io.Discard); err == nil {
		t.Fatal("empty token accepted")
	}
}
