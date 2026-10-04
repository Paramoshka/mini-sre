package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"mini-sre/internal/agent"
)

const (
	DefaultSystemPrompt = "You are mini-sre, a concise SRE assistant running on a Linux host."
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

	history []agent.Message
	usage   agent.Usage
}

func (a *App) Run(ctx context.Context) error {
	a.ensureHistory()

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
	a.ensureHistory()

	a.history = append(a.history, agent.Message{Role: agent.RoleUser, Content: input})
	resp, err := a.turn(ctx)
	if err != nil {
		a.history = a.history[:len(a.history)-1]
		return err
	}
	a.history = append(a.history, agent.Message{Role: agent.RoleAssistant, Content: resp.Content})
	return nil
}

func (a *App) turn(ctx context.Context) (*agent.Response, error) {
	req := agent.Request{Messages: a.history}

	if !a.Stream {
		resp, err := a.Client.Chat(ctx, req)
		if err != nil {
			return nil, err
		}
		fmt.Fprintln(a.Out, resp.Content)
		a.usage = resp.Usage
		a.printUsage()
		return resp, nil
	}

	stream, err := a.Client.ChatStream(ctx, req)
	if err != nil {
		return nil, err
	}

	reasoningOpen := false
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
		}
	}
	if reasoningOpen {
		fmt.Fprintln(a.Err)
	}
	fmt.Fprintln(a.Out)

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
		a.history = a.history[:1]
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

func (a *App) ensureHistory() {
	if a.history != nil {
		return
	}
	promptText := a.SystemPrompt
	if promptText == "" {
		promptText = DefaultSystemPrompt
	}
	a.history = []agent.Message{{Role: agent.RoleSystem, Content: promptText}}
}
