package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"mini-sre/internal/agent"
)

func TestHistoryKeepsWholeToolTurns(t *testing.T) {
	s := &Session{MaxHistoryTurns: 2}
	s.RunTool = func(context.Context, agent.ToolCall) (string, error) { return "checked", nil }
	s.Turn = func(_ context.Context, req agent.Request) (*agent.Response, error) {
		if req.Messages[len(req.Messages)-1].Role == agent.RoleUser {
			return &agent.Response{ToolCalls: []agent.ToolCall{{ID: "call", Name: "probe"}}}, nil
		}
		return &agent.Response{Content: "done"}, nil
	}
	for i := range 4 {
		if _, err := s.Ask(context.Background(), fmt.Sprintf("question %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.History) != 9 || s.History[1].Content != "question 2" || s.History[5].Content != "question 3" {
		t.Fatalf("history dropped part of a turn: %+v", s.History)
	}
	for _, start := range []int{1, 5} {
		if len(s.History[start+1].ToolCalls) != 1 || s.History[start+2].ToolCallID != "call" {
			t.Fatal("tool call/result pair was split")
		}
	}
	s.Reset()
	if len(s.History) != 1 || s.History[0].Role != agent.RoleSystem {
		t.Fatal("reset did not clear history")
	}
}

func TestSessionRollbackAndToolErrors(t *testing.T) {
	s := &Session{}
	s.Turn = func(_ context.Context, req agent.Request) (*agent.Response, error) {
		if req.Messages[len(req.Messages)-1].Role == agent.RoleUser {
			return &agent.Response{ToolCalls: []agent.ToolCall{{ID: "call", Name: "probe"}}}, nil
		}
		if !strings.Contains(req.Messages[len(req.Messages)-1].Content, "permission denied") {
			t.Fatal("tool error was hidden")
		}
		return &agent.Response{Content: "check failed"}, nil
	}
	s.RunTool = func(context.Context, agent.ToolCall) (string, error) { return "", errors.New("permission denied") }
	if _, err := s.Ask(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	base := len(s.History)
	s.Turn = func(context.Context, agent.Request) (*agent.Response, error) {
		return nil, errors.New("model unavailable")
	}
	if _, err := s.Ask(context.Background(), "second"); err == nil || len(s.History) != base {
		t.Fatal("failed model request was not rolled back")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Ask(ctx, "cancel"); !errors.Is(err, context.Canceled) || len(s.History) != base {
		t.Fatal("cancelled request was not rolled back")
	}
}
