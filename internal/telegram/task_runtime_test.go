package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mini-sre/internal/agent"
	"mini-sre/internal/session"
)

func TestTaskPollingKeepsChatAvailableAndSendsSummary(t *testing.T) {
	b, s := testTasks(t)
	task := addTestTask(t, s, "5m", "1h")
	task.State.NextRun, task.State.NextReport = s.now(), s.now()
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	b.newSession = func() *session.Session {
		return &session.Session{
			Turn: func(ctx context.Context, req agent.Request) (*agent.Response, error) {
				if !strings.Contains(req.Messages[0].Content, "scheduled diagnostic") {
					return &agent.Response{Content: "chat answer"}, nil
				}
				if len(req.Messages) == 2 {
					return &agent.Response{ToolCalls: []agent.ToolCall{{ID: "probe", Name: "get_service_status"}}}, nil
				}
				close(started)
				select {
				case <-release:
					return &agent.Response{Content: `{"status":"problem","summary":"nginx inactive"}`}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			},
			RunTool: func(context.Context, agent.ToolCall) (string, error) { return "inactive", nil },
		}
	}
	if err := b.UseState(filepath.Join(filepath.Dir(s.path), "chat.json")); err != nil {
		t.Fatal(err)
	}
	sent := make(chan string, 16)
	var polls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				t.Error(err)
			}
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/getWebhookInfo"):
			io.WriteString(w, `{"ok":true,"result":{"url":""}}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			switch polls.Add(1) {
			case 1:
				select {
				case <-started:
					io.WriteString(w, `{"ok":true,"result":[{"update_id":1,"message":{"text":"hello","from":{"id":1},"chat":{"id":1,"type":"private"}}}]}`)
				case <-r.Context().Done():
				}
			case 2:
				io.WriteString(w, `{"ok":true,"result":[{"update_id":2,"message":{"text":"/task list","from":{"id":1},"chat":{"id":1,"type":"private"}}}]}`)
			default:
				<-r.Context().Done()
			}
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var message sentMessage
			if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
				t.Error(err)
			}
			io.WriteString(w, `{"ok":true,"result":{}}`)
			sent <- message.Text
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	b.baseURL = server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	chat, command := false, false
	for !chat || !command {
		select {
		case text := <-sent:
			chat = chat || text == "chat answer"
			command = command || strings.HasPrefix(text, task.ID+" ·")
		case <-ctx.Done():
			t.Fatal("chat or task command blocked behind scheduled model request")
		}
	}
	close(release)
	incident, report := false, false
	for !incident || !report {
		select {
		case text := <-sent:
			incident = incident || strings.Contains(text, ": проблема")
			report = report || strings.Contains(text, "Выполнено: 1; норма: 0; проблемы: 1")
		case <-ctx.Done():
			t.Fatal("incident or summary was not delivered")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := decodeTaskFile(data)
	if err != nil || saved.Tasks[0].State.LastResult.Status != "problem" || saved.Tasks[0].State.Running {
		t.Fatalf("model result was not checkpointed: %+v %v", saved, err)
	}
}

func TestTaskWorkerCancellationLeavesRecoverableRun(t *testing.T) {
	b, s := testTasks(t)
	task := addTestTask(t, s, "5m", "1h")
	task.State.NextRun = s.now()
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	b.newSession = func() *session.Session {
		return &session.Session{Turn: func(ctx context.Context, _ agent.Request) (*agent.Response, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.run(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("check did not start")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := b.UseTasks(s.path); err != nil {
		t.Fatal(err)
	}
	if b.tasks.file.Tasks[0].State.Counts.Interrupted != 1 {
		t.Fatal("shutdown fabricated a completed check instead of retaining interrupted state")
	}
}

func TestTaskWatcherDirectoryRemovalIsFatal(t *testing.T) {
	_, s := testTasks(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.run(ctx) }()
	// A command reply establishes that the watcher has been registered.
	if _, err := s.command(ctx, privateMessage(t, 1, "/task"), 1, "/task"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(s.path)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "directory") {
			t.Fatalf("watcher error not propagated: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("watcher ignored directory removal")
	}
}

func TestTaskStateWriteFailureIsFatal(t *testing.T) {
	_, s := testTasks(t)
	task := addTestTask(t, s, "5m", "1h")
	// A regular file cannot be used as the parent directory of a checkpoint.
	s.path = filepath.Join(s.path, "cannot-write.json")
	err := s.finish(taskCompletion{*task, taskOutcome{"ok", "checked"}})
	var stateErr *stateError
	if !errors.As(err, &stateErr) {
		t.Fatalf("write failure was hidden: %v", err)
	}
}
