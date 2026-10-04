package agent

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

const streamEvents = `data: {"id":"chat-1","object":"chat.completion.chunk","created":1735689600,"model":"deepseek-flash","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"chat-1","object":"chat.completion.chunk","created":1735689600,"model":"deepseek-flash","choices":[{"index":0,"delta":{"reasoning_content":"thin"},"finish_reason":null}]}

data: {"id":"chat-1","object":"chat.completion.chunk","created":1735689600,"model":"deepseek-flash","choices":[{"index":0,"delta":{"reasoning_content":"king"},"finish_reason":null}]}

data: {"id":"chat-1","object":"chat.completion.chunk","created":1735689600,"model":"deepseek-flash","choices":[{"index":0,"delta":{"content":"po"},"finish_reason":null}]}

data: {"id":"chat-1","object":"chat.completion.chunk","created":1735689600,"model":"deepseek-flash","choices":[{"index":0,"delta":{"content":"ng"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_hit_tokens":7,"prompt_cache_miss_tokens":3,"completion_tokens_details":{"reasoning_tokens":2}}}

data: [DONE]

`

const streamMalformed = `data: {"id":"chat-1","object":"chat.completion.chunk","created":1735689600,"model":"deepseek-flash","choices":[{"index":0,"delta":{"content":"first"},"finish_reason":null}]}

data: {not json}

data: [DONE]

`

const streamToolCalls = `data: {"id":"chat-4","object":"chat.completion.chunk","created":1735689600,"model":"deepseek-flash","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_disk_usage","arguments":"{\"path\":"}}]},"finish_reason":null}]}

data: {"id":"chat-4","object":"chat.completion.chunk","created":1735689600,"model":"deepseek-flash","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"/\"}"}}]},"finish_reason":null}]}

data: {"id":"chat-4","object":"chat.completion.chunk","created":1735689600,"model":"deepseek-flash","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}

data: [DONE]

`

func sseHandler(cap *capture, payload string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := cap.record(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, payload)
		w.(http.Flusher).Flush()
	}
}

func TestChatStream(t *testing.T) {
	var cap capture
	client := newTestClient(t, Config{}, sseHandler(&cap, streamEvents))

	stream, err := client.ChatStream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var chunks []Chunk
	for chunk, err := range stream.Chunks() {
		if err != nil {
			t.Fatalf("Chunks: %v", err)
		}
		chunks = append(chunks, chunk)
	}

	if len(chunks) != 5 {
		t.Fatalf("chunks = %d, want 5", len(chunks))
	}
	if chunks[1].ReasoningContent != "thin" || chunks[2].ReasoningContent != "king" {
		t.Errorf("reasoning deltas = %q/%q, want thin/king", chunks[1].ReasoningContent, chunks[2].ReasoningContent)
	}
	if chunks[3].Content != "po" || chunks[4].Content != "ng" {
		t.Errorf("content deltas = %q/%q, want po/ng", chunks[3].Content, chunks[4].Content)
	}
	if chunks[4].FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want %q", chunks[4].FinishReason, "stop")
	}

	resp := stream.Response()
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

	got := cap.snapshot()
	if len(got.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(got.requests))
	}
	if !got.requests[0].Stream {
		t.Error("stream = false, want true")
	}
	if !got.requests[0].StreamOptions.IncludeUsage {
		t.Error("stream_options.include_usage = false, want true")
	}
	if got.requests[0].Thinking.Type != string(ThinkingDisabled) {
		t.Errorf("thinking.type = %q, want %q", got.requests[0].Thinking.Type, ThinkingDisabled)
	}
}

func TestChatStreamToolCalls(t *testing.T) {
	var cap capture
	client := newTestClient(t, Config{}, sseHandler(&cap, streamToolCalls))

	stream, err := client.ChatStream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "disk?"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	for _, err := range stream.Chunks() {
		if err != nil {
			t.Fatalf("Chunks: %v", err)
		}
	}

	resp := stream.Response()
	if resp.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "tool_calls")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %d, want 1", len(resp.ToolCalls))
	}
	call := resp.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "get_disk_usage" || call.Arguments != `{"path":"/"}` {
		t.Errorf("ToolCall = %+v, want call_1/get_disk_usage with joined arguments", call)
	}
}

func TestChatStreamMidStreamError(t *testing.T) {
	var cap capture
	client := newTestClient(t, Config{}, sseHandler(&cap, streamMalformed))

	stream, err := client.ChatStream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var chunks []Chunk
	var streamErr error
	for chunk, err := range stream.Chunks() {
		if err != nil {
			streamErr = err
			break
		}
		chunks = append(chunks, chunk)
	}

	if len(chunks) != 1 || chunks[0].Content != "first" {
		t.Errorf("chunks = %+v, want single %q chunk", chunks, "first")
	}
	if streamErr == nil {
		t.Fatal("stream error = nil, want error")
	}
	if stream.Response().Content != "first" {
		t.Errorf("partial Response.Content = %q, want %q", stream.Response().Content, "first")
	}
}

func TestChatStreamImmediateError(t *testing.T) {
	var cap capture
	client := newTestClient(t, Config{MaxRetries: intPtr(0), Timeout: 5 * time.Second}, func(w http.ResponseWriter, r *http.Request) {
		if _, err := cap.record(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	if _, err := client.ChatStream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "ping"}},
	}); err == nil {
		t.Fatal("ChatStream() = nil error, want error")
	}
	if got := cap.snapshot(); len(got.requests) != 1 {
		t.Errorf("requests = %d, want 1", len(got.requests))
	}
}

func TestChatStreamEarlyBreak(t *testing.T) {
	closed := make(chan struct{})
	var cap capture
	client := newTestClient(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		if _, err := cap.record(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `data: {"id":"chat-1","object":"chat.completion.chunk","created":1735689600,"model":"deepseek-flash","choices":[{"index":0,"delta":{"content":"first"},"finish_reason":null}]}`+"\n\n")
		w.(http.Flusher).Flush()

		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
		close(closed)
	})

	stream, err := client.ChatStream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var received int
	for chunk, err := range stream.Chunks() {
		if err != nil {
			t.Fatalf("Chunks: %v", err)
		}
		received++
		if chunk.Content == "first" {
			break
		}
	}
	if received != 1 {
		t.Errorf("received = %d, want 1", received)
	}
	if stream.Response().Content != "first" {
		t.Errorf("partial Response.Content = %q, want %q", stream.Response().Content, "first")
	}

	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not observe stream close after break")
	}
}
