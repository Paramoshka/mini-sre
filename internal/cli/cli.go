package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"mini-sre/internal/agent"
	"mini-sre/internal/session"
)

const (
	DefaultSystemPrompt = session.DefaultSystemPrompt
	prompt              = "mini-sre> "
)

type Chatter interface {
	ChatStream(ctx context.Context, req agent.Request) (*agent.Stream, error)
	Chat(ctx context.Context, req agent.Request) (*agent.Response, error)
}

type App struct {
	Client       Chatter
	In           io.Reader
	Out          io.Writer
	Err          io.Writer
	SystemPrompt string
	Stream       bool
	Reasoning    bool
	Tools        []agent.Tool
	RunTool      func(ctx context.Context, call agent.ToolCall) (string, error)

	session *session.Session
	usage   agent.Usage
}

func (a *App) Run(ctx context.Context) error {
	a.ensureSession()

	scanner := bufio.NewScanner(a.In)
	for {
		fmt.Fprint(a.Out, prompt)
		if !scanner.Scan() {
			fmt.Fprintln(a.Out)
			return scanner.Err()
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			if a.handleCommand(line) {
				return nil
			}
			continue
		}
		if err := a.Ask(ctx, line); err != nil {
			fmt.Fprintf(a.Err, "error: %v\n", err)
		}
	}
}

func (a *App) Ask(ctx context.Context, input string) error {
	a.ensureSession()
	_, err := a.session.Ask(ctx, input)
	return err
}

func (a *App) turn(ctx context.Context, req agent.Request) (*agent.Response, error) {
	if !a.Stream {
		resp, err := a.Client.Chat(ctx, req)
		if err != nil {
			return nil, err
		}
		if resp.Content != "" {
			fmt.Fprintln(a.Out, resp.Content)
		}
		a.usage = resp.Usage
		a.printUsage()
		return resp, nil
	}

	stream, err := a.Client.ChatStream(ctx, req)
	if err != nil {
		return nil, err
	}

	reasoningOpen := false
	wroteContent := false
	for chunk, err := range stream.Chunks() {
		if err != nil {
			return nil, err
		}
		if a.Reasoning && chunk.ReasoningContent != "" {
			fmt.Fprint(a.Err, chunk.ReasoningContent)
			reasoningOpen = true
		}
		if chunk.Content != "" {
			if reasoningOpen {
				fmt.Fprintln(a.Err)
				reasoningOpen = false
			}
			fmt.Fprint(a.Out, chunk.Content)
			wroteContent = true
		}
	}
	if reasoningOpen {
		fmt.Fprintln(a.Err)
	}
	if wroteContent {
		fmt.Fprintln(a.Out)
	}

	resp := stream.Response()
	a.usage = resp.Usage
	a.printUsage()
	return &resp, nil
}

func (a *App) handleCommand(line string) (quit bool) {
	switch line {
	case "/exit", "/quit":
		return true
	case "/clear":
		a.session.Reset()
	case "/usage":
		fmt.Fprintf(a.Out, "tokens: prompt=%d completion=%d total=%d, cache: hit=%d miss=%d\n",
			a.usage.PromptTokens, a.usage.CompletionTokens, a.usage.TotalTokens,
			a.usage.CacheHitTokens, a.usage.CacheMissTokens)
	default:
		fmt.Fprintf(a.Err, "unknown command: %s\n", line)
	}
	return false
}

func (a *App) printUsage() {
	if a.usage.TotalTokens == 0 {
		return
	}
	fmt.Fprintf(a.Err, "[tokens prompt=%d completion=%d total=%d, cache hit=%d miss=%d]\n",
		a.usage.PromptTokens, a.usage.CompletionTokens, a.usage.TotalTokens,
		a.usage.CacheHitTokens, a.usage.CacheMissTokens)
}

func (a *App) ensureSession() {
	if a.session != nil {
		return
	}
	a.session = &session.Session{
		Turn: a.turn, Tools: a.Tools, RunTool: a.RunTool, SystemPrompt: a.SystemPrompt,
		OnToolCall: func(call agent.ToolCall) {
			fmt.Fprintf(a.Err, "→ %s(%s)\n", call.Name, call.Arguments)
		},
	}
	a.session.Reset()
}
