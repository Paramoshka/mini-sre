package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTelegramHTTPProxy(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			var calls atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNz" {
					t.Error("missing proxy credentials")
				}
				if scheme == "https" {
					if r.Method != http.MethodConnect || r.Host != "telegram.invalid:443" {
						t.Errorf("unexpected CONNECT request: %s %s", r.Method, r.Host)
					}
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				if r.URL.Host != "telegram.invalid" || r.Method != http.MethodPost {
					t.Errorf("unexpected proxied request: %s %s", r.Method, r.URL.Host)
				}
				io.WriteString(w, `{"ok":true,"result":{}}`)
			}))
			defer proxy.Close()
			proxyURL, err := url.Parse(proxy.URL)
			if err != nil {
				t.Fatal(err)
			}
			proxyURL.User = url.UserPassword("user", "pass")
			t.Setenv("TELEGRAM_HTTP_PROXY", proxyURL.String())
			defaultTransport := http.DefaultTransport
			bot, err := New("secret-token", func() []int64 { return []int64{1} }, nil, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			defer bot.client.CloseIdleConnections()
			if bot.client.Transport == defaultTransport || http.DefaultTransport != defaultTransport {
				t.Fatal("proxy configuration did not use a separate transport")
			}
			bot.baseURL = scheme + "://telegram.invalid"
			err = bot.call(context.Background(), "getUpdates", struct{}{}, nil)
			if calls.Load() != 1 || (scheme == "http" && err != nil) || (scheme == "https" && err == nil) {
				t.Fatalf("proxy calls=%d error=%v", calls.Load(), err)
			}
			if err != nil && (strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "user:pass")) {
				t.Fatalf("proxy error leaked credentials: %v", err)
			}
		})
	}
}

func TestTelegramHTTPProxyValidation(t *testing.T) {
	for _, proxy := range []string{"localhost:8080", "socks5://localhost:1080", "http://", "http://user:secret@%invalid"} {
		t.Setenv("TELEGRAM_HTTP_PROXY", proxy)
		_, err := New("token", func() []int64 { return []int64{1} }, nil, io.Discard)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid proxy accepted or credentials leaked: %v", err)
		}
	}
	t.Setenv("TELEGRAM_HTTP_PROXY", "")
	bot, err := New("token", func() []int64 { return []int64{1} }, nil, io.Discard)
	if err != nil || bot.client.Transport != nil {
		t.Fatalf("default transport behavior changed: %v", err)
	}
}

func TestRateLimitAndTokenRedaction(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(429)
		io.WriteString(w, `{"ok":false,"error_code":429,"description":"secret-token rate limited","parameters":{"retry_after":3600}}`)
	}))
	defer server.Close()
	bot, err := New("secret-token", func() []int64 { return []int64{1} }, nil, io.Discard)
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
			bot, err := New("secret-token", func() []int64 { return []int64{1} }, nil, io.Discard)
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
		if _, err := New("token", func() []int64 { return users }, nil, io.Discard); err == nil {
			t.Fatal("invalid allowlist accepted")
		}
	}
	if _, err := New("", func() []int64 { return []int64{1} }, nil, io.Discard); err == nil {
		t.Fatal("empty token accepted")
	}
}
