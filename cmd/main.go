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
	"mini-sre/internal/config"
	"mini-sre/internal/dotenv"
	"mini-sre/internal/remote"
	"mini-sre/internal/session"
	"mini-sre/internal/telegram"
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
		model        = flag.String("model", "", "model ID (default deepseek-flash)")
		baseURL      = flag.String("base-url", "", "API base URL (default https://api.deepseek.com)")
		temperature  = flag.Float64("temperature", -1, "sampling temperature 0..2 (-1 = server default)")
		thinking     = flag.Bool("thinking", false, "enable thinking mode")
		reasoning    = flag.Bool("reasoning", false, "print reasoning content to stderr")
		stream       = flag.Bool("stream", true, "stream tokens as they arrive")
		configPath   = flag.String("config", "", "host and Telegram configuration YAML (default local only)")
		telegramMode = flag.Bool("telegram", false, "run the Telegram bot instead of the console")
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

	if err := dotenv.Load(".env"); err != nil {
		return err
	}
	hostConfig, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *telegramMode && (*configPath == "" || len(flag.Args()) != 0) {
		return fmt.Errorf("telegram mode requires -config and does not accept a positional prompt")
	}
	runner := &remote.Runner{Config: hostConfig}

	client, err := agent.New(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *telegramMode {
		bot, err := telegram.New(os.Getenv("TELEGRAM_BOT_TOKEN"), hostConfig.Telegram.AllowedUserIDs,
			func() *session.Session {
				return &session.Session{Turn: client.Chat, Tools: toolSpecs(), RunTool: toolRunner(runner)}
			}, os.Stderr)
		if err != nil {
			return err
		}
		return bot.Run(ctx)
	}

	app := &cli.App{
		Client:       client,
		In:           os.Stdin,
		Out:          os.Stdout,
		Err:          os.Stderr,
		SystemPrompt: cli.DefaultSystemPrompt,
		Stream:       *stream,
		Reasoning:    *reasoning,
		Tools:        toolSpecs(),
		RunTool:      toolRunner(runner),
	}

	if args := flag.Args(); len(args) > 0 {
		return app.Ask(ctx, strings.Join(args, " "))
	}
	if err := app.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
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

func toolRunner(runner *remote.Runner) func(ctx context.Context, call agent.ToolCall) (string, error) {
	registry := tools.Registry(runner)
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
