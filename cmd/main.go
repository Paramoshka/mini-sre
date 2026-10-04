package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"mini-sre/internal/agent"
	"mini-sre/internal/cli"
	"mini-sre/internal/tools"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mini-sre:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		model       = flag.String("model", "", "model ID (default deepseek-flash)")
		baseURL     = flag.String("base-url", "", "API base URL (default https://api.deepseek.com)")
		temperature = flag.Float64("temperature", -1, "sampling temperature 0..2 (-1 = server default)")
		thinking    = flag.Bool("thinking", false, "enable thinking mode")
		reasoning   = flag.Bool("reasoning", false, "print reasoning content to stderr")
		stream      = flag.Bool("stream", true, "stream tokens as they arrive")
	)
	flag.Parse()

	cfg := agent.Config{
		Model:   *model,
		BaseURL: *baseURL,
	}
	if *temperature >= 0 {
		cfg.Temperature = temperature
	}
	if *thinking {
		cfg.Thinking = agent.ThinkingEnabled
	}

	client, err := agent.New(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := &cli.App{
		Client:       client,
		In:           os.Stdin,
		Out:          os.Stdout,
		Err:          os.Stderr,
		SystemPrompt: cli.DefaultSystemPrompt,
		Stream:       *stream,
		Reasoning:    *reasoning,
		Tools:        toolSpecs(),
		RunTool:      toolRunner(),
	}

	if args := flag.Args(); len(args) > 0 {
		return app.Ask(ctx, strings.Join(args, " "))
	}
	return app.Run(ctx)
}

func toolSpecs() []agent.Tool {
	specs := tools.Specs()
	out := make([]agent.Tool, 0, len(specs))
	for _, spec := range specs {
		out = append(out, agent.Tool{
			Name:        spec.Name,
			Description: spec.Description,
			Parameters:  spec.Parameters,
		})
	}
	return out
}

func toolRunner() func(ctx context.Context, call agent.ToolCall) (string, error) {
	registry := tools.Registry()
	return func(ctx context.Context, call agent.ToolCall) (string, error) {
		run, ok := registry[call.Name]
		if !ok {
			return "", fmt.Errorf("unknown tool %q", call.Name)
		}
		var args json.RawMessage
		if call.Arguments != "" {
			args = json.RawMessage(call.Arguments)
		}
		return run(ctx, args)
	}
}
