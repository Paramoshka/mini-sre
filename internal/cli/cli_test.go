package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"mini-sre/internal/agent"
)

const streamBody = `data: {"id":"chat-1","object":"chat.completion.chunk","created":1,"model":"deepseek-flash","choices":[{"index":0,"delta":{"reasoning_content":"think "},"finish_reason":null}]}

data: {"id":"chat-1","object":"chat.completion.chunk","created":1,"model":"deepseek-flash","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}

data: {"id":"chat-1","object":"chat.completion.chunk","created":1,"model":"deepseek-flash","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_hit_tokens":7,"prompt_cache_miss_tokens":3}}

data: [DONE]

`

const completionBody = `{
  "id": "chat-2",
  "object": "chat.completion",
  "created": 1,
  "model": "deepseek-flash",
  "choices": [{"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": "pong"}}],
  "usage": {"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4, "prompt_cache_hit_tokens": 0, "prompt_cache_miss_tokens": 3}
}`

type messageJSON struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type recorder struct {
	mu       sync.Mutex
	requests [][]messageJSON
}

func (r *recorder) add(messages []messageJSON) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, messages)
}

func (r *recorder) snapshot() [][]messageJSON {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]messageJSON(nil), r.requests...)
}

func testClient(t *testing.T, rec *recorder, body string, contentType string) *agent.AgentClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []messageJSON `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rec.add(req.Messages)
		w.Header().Set("Content-Type", contentType)
		io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)

	client, err := agent.New(agent.Config{APIKey: "test-key", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return client
}

func TestAskStreamsAnswer(t *testing.T) {
	var rec recorder
	client := testClient(t, &rec, streamBody, "text/event-stream")

	var out, errOut bytes.Buffer
	app := &App{Client: client, Out: &out, Err: &errOut, Stream: true, Reasoning: true}

	if err := app.Ask(context.Background(), "hi"); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if out.String() != "hello world\n" {
		t.Errorf("stdout = %q, want %q", out.String(), "hello world\n")
	}
	if !strings.Contains(errOut.String(), "think ") {
		t.Errorf("stderr = %q, want reasoning text", errOut.String())
	}
	if !strings.Contains(errOut.String(), "cache hit=7 miss=3") {
		t.Errorf("stderr = %q, want cache stats", errOut.String())
	}

	if len(app.history) != 3 {
		t.Fatalf("history = %d messages, want 3", len(app.history))
	}
	if app.history[0].Role != agent.RoleSystem || app.history[0].Content != DefaultSystemPrompt {
		t.Errorf("history[0] = %+v, want system %q", app.history[0], DefaultSystemPrompt)
	}
	if app.history[1].Role != agent.RoleUser || app.history[1].Content != "hi" {
		t.Errorf("history[1] = %+v, want user/hi", app.history[1])
	}
	if app.history[2].Role != agent.RoleAssistant || app.history[2].Content != "hello world" {
		t.Errorf("history[2] = %+v, want assistant/hello world", app.history[2])
	}

	reqs := rec.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	if len(reqs[0]) != 2 || reqs[0][0].Role != "system" || reqs[0][1].Content != "hi" {
		t.Errorf("request messages = %+v, want system + hi", reqs[0])
	}
}

func TestAskNonStreaming(t *testing.T) {
	var rec recorder
	client := testClient(t, &rec, completionBody, "application/json")

	var out, errOut bytes.Buffer
	app := &App{Client: client, Out: &out, Err: &errOut, Stream: false}

	if err := app.Ask(context.Background(), "ping"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if out.String() != "pong\n" {
		t.Errorf("stdout = %q, want %q", out.String(), "pong\n")
	}
}

func TestAskRollsBackHistoryOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	client, err := agent.New(agent.Config{APIKey: "test-key", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}

	var out, errOut bytes.Buffer
	app := &App{Client: client, Out: &out, Err: &errOut, Stream: true}

	if err := app.Ask(context.Background(), "hi"); err == nil {
		t.Fatal("Ask() = nil error, want error")
	}
	if len(app.history) != 1 || app.history[0].Role != agent.RoleSystem {
		t.Errorf("history = %+v, want only system message", app.history)
	}
}

func TestRunCommands(t *testing.T) {
	var rec recorder
	client := testClient(t, &rec, streamBody, "text/event-stream")

	var out, errOut bytes.Buffer
	app := &App{
		Client: client,
		In:     strings.NewReader("hi\n/usage\n/exit\n"),
		Out:    &out,
		Err:    &errOut,
		Stream: true,
	}

	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.Count(out.String(), prompt); got != 3 {
		t.Errorf("prompts = %d, want 3", got)
	}
	if !strings.Contains(out.String(), "hello world") {
		t.Errorf("stdout = %q, want answer", out.String())
	}
	if !strings.Contains(out.String(), "tokens: prompt=10 completion=5 total=15, cache: hit=7 miss=3") {
		t.Errorf("stdout = %q, want /usage output", out.String())
	}
	if got := len(rec.snapshot()); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func TestRunClear(t *testing.T) {
	var rec recorder
	client := testClient(t, &rec, streamBody, "text/event-stream")

	var out, errOut bytes.Buffer
	app := &App{
		Client: client,
		In:     strings.NewReader("hi\n/clear\n/exit\n"),
		Out:    &out,
		Err:    &errOut,
		Stream: true,
	}

	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(app.history) != 1 || app.history[0].Role != agent.RoleSystem {
		t.Errorf("history = %+v, want only system message after /clear", app.history)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var rec recorder
	client := testClient(t, &rec, streamBody, "text/event-stream")

	var out, errOut bytes.Buffer
	app := &App{
		Client: client,
		In:     strings.NewReader("/nope\n/exit\n"),
		Out:    &out,
		Err:    &errOut,
		Stream: true,
	}

	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(errOut.String(), "unknown command: /nope") {
		t.Errorf("stderr = %q, want unknown command error", errOut.String())
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}
}
