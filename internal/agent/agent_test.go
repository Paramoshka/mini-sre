package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const completionJSON = `{
  "id": "chat-1",
  "object": "chat.completion",
  "created": 1735689600,
  "model": "deepseek-flash",
  "choices": [{
    "index": 0,
    "finish_reason": "stop",
    "message": {"role": "assistant", "content": "pong", "reasoning_content": "thinking"}
  }],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 5,
    "total_tokens": 15,
    "prompt_cache_hit_tokens": 7,
    "prompt_cache_miss_tokens": 3,
    "completion_tokens_details": {"reasoning_tokens": 2}
  }
}`

const completionJSONNoCache = `{
  "id": "chat-2",
  "object": "chat.completion",
  "created": 1735689600,
  "model": "deepseek-flash",
  "choices": [{
    "index": 0,
    "finish_reason": "stop",
    "message": {"role": "assistant", "content": "ok"}
  }],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`

type requestBody struct {
	Model         string        `json:"model"`
	Messages      []messageBody `json:"messages"`
	Temperature   *float64      `json:"temperature"`
	MaxTokens     *int          `json:"max_tokens"`
	Stream        bool          `json:"stream"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	Thinking struct {
		Type string `json:"type"`
	} `json:"thinking"`
}

type messageBody struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type capture struct {
	mu       sync.Mutex
	requests []requestBody
	auths    []string
	paths    []string
}

func (c *capture) record(r *http.Request) (int, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return 0, err
	}
	var parsed requestBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, parsed)
	c.auths = append(c.auths, r.Header.Get("Authorization"))
	c.paths = append(c.paths, r.URL.Path)
	return len(c.requests), nil
}

type snapshot struct {
	requests []requestBody
	auths    []string
	paths    []string
}

func (c *capture) snapshot() snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return snapshot{
		requests: append([]requestBody(nil), c.requests...),
		auths:    append([]string(nil), c.auths...),
		paths:    append([]string(nil), c.paths...),
	}
}

func newTestClient(t *testing.T, cfg Config, handler http.HandlerFunc) *AgentClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	if cfg.APIKey == "" {
		cfg.APIKey = "test-key"
	}
	cfg.BaseURL = server.URL
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func jsonHandler(cap *capture, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := cap.record(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}
}

func floatPtr(v float64) *float64 { return &v }
func intPtr(v int) *int           { return &v }

func TestNew(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("DEEPSEEK_API_KEY", "")
		client, err := New(Config{APIKey: "k"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if client.cfg.BaseURL != DefaultBaseURL {
			t.Errorf("BaseURL = %q, want %q", client.cfg.BaseURL, DefaultBaseURL)
		}
		if client.cfg.Model != DefaultModel {
			t.Errorf("Model = %q, want %q", client.cfg.Model, DefaultModel)
		}
		if client.cfg.Thinking != ThinkingDisabled {
			t.Errorf("Thinking = %q, want %q", client.cfg.Thinking, ThinkingDisabled)
		}
		if client.cfg.Timeout != DefaultTimeout {
			t.Errorf("Timeout = %v, want %v", client.cfg.Timeout, DefaultTimeout)
		}
		if client.cfg.MaxRetries == nil || *client.cfg.MaxRetries != DefaultMaxRetries {
			t.Errorf("MaxRetries = %v, want %d", client.cfg.MaxRetries, DefaultMaxRetries)
		}
		if client.cfg.Temperature != nil {
			t.Errorf("Temperature = %v, want nil", client.cfg.Temperature)
		}
	})

	t.Run("api key from env", func(t *testing.T) {
		t.Setenv("DEEPSEEK_API_KEY", "from-env")
		client, err := New(Config{})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if client.cfg.APIKey != "from-env" {
			t.Errorf("APIKey = %q, want %q", client.cfg.APIKey, "from-env")
		}
	})

	t.Run("missing api key", func(t *testing.T) {
		t.Setenv("DEEPSEEK_API_KEY", "")
		if _, err := New(Config{}); err == nil {
			t.Fatal("New() = nil error, want error")
		}
	})

	t.Run("negative max retries", func(t *testing.T) {
		if _, err := New(Config{APIKey: "k", MaxRetries: intPtr(-1)}); err == nil {
			t.Fatal("New() = nil error, want error")
		}
	})
}

func TestChatRequest(t *testing.T) {
	var cap capture
	client := newTestClient(t, Config{
		Temperature: floatPtr(0.2),
		MaxTokens:   128,
	}, jsonHandler(&cap, completionJSON))

	resp, err := client.Chat(context.Background(), Request{
		Messages: []Message{
			{Role: RoleSystem, Content: "be brief"},
			{Role: RoleUser, Content: "ping"},
		},
		MaxTokens: intPtr(64),
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	got := cap.snapshot()
	if len(got.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(got.requests))
	}
	if got.paths[0] != "/chat/completions" {
		t.Errorf("path = %q, want %q", got.paths[0], "/chat/completions")
	}
	if got.auths[0] != "Bearer test-key" {
		t.Errorf("Authorization = %q, want %q", got.auths[0], "Bearer test-key")
	}

	req := got.requests[0]
	if req.Model != DefaultModel {
		t.Errorf("model = %q, want %q", req.Model, DefaultModel)
	}
	if req.Temperature == nil || *req.Temperature != 0.2 {
		t.Errorf("temperature = %v, want 0.2", req.Temperature)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 64 {
		t.Errorf("max_tokens = %v, want 64", req.MaxTokens)
	}
	if req.Thinking.Type != string(ThinkingDisabled) {
		t.Errorf("thinking.type = %q, want %q", req.Thinking.Type, ThinkingDisabled)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(req.Messages))
	}
	if req.Messages[0].Role != string(RoleSystem) || req.Messages[0].Content != "be brief" {
		t.Errorf("message[0] = %+v, want system/be brief", req.Messages[0])
	}
	if req.Messages[1].Role != string(RoleUser) || req.Messages[1].Content != "ping" {
		t.Errorf("message[1] = %+v, want user/ping", req.Messages[1])
	}

	if resp.ID != "chat-1" {
		t.Errorf("ID = %q, want %q", resp.ID, "chat-1")
	}
	if resp.Model != DefaultModel {
		t.Errorf("Model = %q, want %q", resp.Model, DefaultModel)
	}
	if resp.Content != "pong" {
		t.Errorf("Content = %q, want %q", resp.Content, "pong")
	}
	if resp.ReasoningContent != "thinking" {
		t.Errorf("ReasoningContent = %q, want %q", resp.ReasoningContent, "thinking")
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "stop")
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 5 || resp.Usage.TotalTokens != 15 {
		t.Errorf("token usage = %+v, want 10/5/15", resp.Usage)
	}
	if resp.Usage.CacheHitTokens != 7 || resp.Usage.CacheMissTokens != 3 {
		t.Errorf("cache usage = hit %d/miss %d, want 7/3", resp.Usage.CacheHitTokens, resp.Usage.CacheMissTokens)
	}
	if resp.Usage.ReasoningTokens != 2 {
		t.Errorf("ReasoningTokens = %d, want 2", resp.Usage.ReasoningTokens)
	}
}

func TestChatThinkingEnabled(t *testing.T) {
	var cap capture
	client := newTestClient(t, Config{Thinking: ThinkingEnabled}, jsonHandler(&cap, completionJSON))

	if _, err := client.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "ping"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	got := cap.snapshot()
	if len(got.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(got.requests))
	}
	if got.requests[0].Thinking.Type != string(ThinkingEnabled) {
		t.Errorf("thinking.type = %q, want %q", got.requests[0].Thinking.Type, ThinkingEnabled)
	}
}

func TestChatResponseWithoutCacheFields(t *testing.T) {
	var cap capture
	client := newTestClient(t, Config{}, jsonHandler(&cap, completionJSONNoCache))

	resp, err := client.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("Content = %q, want %q", resp.Content, "ok")
	}
	if resp.ReasoningContent != "" {
		t.Errorf("ReasoningContent = %q, want empty", resp.ReasoningContent)
	}
	if resp.Usage.CacheHitTokens != 0 || resp.Usage.CacheMissTokens != 0 || resp.Usage.ReasoningTokens != 0 {
		t.Errorf("Usage = %+v, want zero cache/reasoning fields", resp.Usage)
	}
}

func TestChatValidation(t *testing.T) {
	var cap capture
	client := newTestClient(t, Config{}, jsonHandler(&cap, completionJSON))

	if _, err := client.Chat(context.Background(), Request{}); err == nil {
		t.Error("Chat(no messages) = nil error, want error")
	}
	if _, err := client.Chat(context.Background(), Request{
		Messages: []Message{{Role: "unknown", Content: "ping"}},
	}); err == nil {
		t.Error("Chat(unknown role) = nil error, want error")
	}
	if got := cap.snapshot(); len(got.requests) != 0 {
		t.Errorf("requests = %d, want 0", len(got.requests))
	}
}

func TestChatRetries(t *testing.T) {
	t.Run("retries then succeeds", func(t *testing.T) {
		var cap capture
		client := newTestClient(t, Config{MaxRetries: intPtr(2), Timeout: 5 * time.Second}, func(w http.ResponseWriter, r *http.Request) {
			attempt, err := cap.record(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if attempt <= 2 {
				w.Header().Set("Retry-After-Ms", "0")
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, completionJSON)
		})

		resp, err := client.Chat(context.Background(), Request{
			Messages: []Message{{Role: RoleUser, Content: "ping"}},
		})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if resp.Content != "pong" {
			t.Errorf("Content = %q, want %q", resp.Content, "pong")
		}
		if got := cap.snapshot(); len(got.requests) != 3 {
			t.Errorf("requests = %d, want 3", len(got.requests))
		}
	})

	t.Run("retries disabled", func(t *testing.T) {
		var cap capture
		client := newTestClient(t, Config{MaxRetries: intPtr(0), Timeout: 5 * time.Second}, func(w http.ResponseWriter, r *http.Request) {
			if _, err := cap.record(r); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Retry-After-Ms", "0")
			http.Error(w, "boom", http.StatusInternalServerError)
		})

		if _, err := client.Chat(context.Background(), Request{
			Messages: []Message{{Role: RoleUser, Content: "ping"}},
		}); err == nil {
			t.Fatal("Chat() = nil error, want error")
		}
		if got := cap.snapshot(); len(got.requests) != 1 {
			t.Errorf("requests = %d, want 1", len(got.requests))
		}
	})
}
