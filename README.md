# mini-sre

A minimal SRE agent for DeepSeek (OpenAI-compatible API): a CLI chat with streaming,
history, and function calling.

## Features

- REPL and one-shot mode, the answer is printed as it is generated;
- tool calls: the model itself calls the read-only probes `get_load_average` and
  `get_disk_usage(path)`, the CLI executes them on the host;
- token and DeepSeek server cache stats after every answer;
- DeepSeek thinking mode (disabled by default).

## Requirements

- Go 1.25+;
- Linux (`get_disk_usage` uses `syscall.Statfs`);
- a DeepSeek API key.

## Usage

```bash
export DEEPSEEK_API_KEY=sk-...

go run ./cmd "what is the load average and how much space is on /?"   # one-shot
go run ./cmd                                                          # interactive REPL
```

Build:

```bash
go build -o mini-sre ./cmd
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `-model` | `deepseek-flash` | model ID (`deepseek-flash`, `deepseek-v4-pro`) |
| `-base-url` | `https://api.deepseek.com` | API base URL |
| `-temperature` | server default (1.0) | 0..2; ignored in thinking mode |
| `-thinking` | `false` | enable thinking mode |
| `-reasoning` | `false` | print `reasoning_content` to stderr |
| `-stream` | `true` | print the answer as it is generated |

The key is read from the `DEEPSEEK_API_KEY` environment variable, never from
arguments. On startup the CLI loads `.env` from the current directory if present;
real environment variables take precedence over the file. A malformed `.env`
aborts startup.

## REPL commands

| Command | Action |
|---|---|
| `/usage` | tokens and cache hit/miss of the last answer |
| `/clear` | clear history (the system prompt is kept) |
| `/exit`, `/quit` | quit |

## Development

```bash
gofmt -l .
go build ./...
go vet ./...
go test -race ./...
```

Layout:

- `internal/agent` - DeepSeek client: Chat, ChatStream (iterator), tool calls, its own
  types, the SDK does not leak outside;
- `internal/cli` - REPL, one-shot, agent loop (up to 5 tool call rounds);
- `internal/tools` - host probes and their JSON schemas for the model;
- `cmd/main.go` - flags and wiring.

## Notes

- DeepSeek context caching is server-side and automatic, there is no API setting; only
  `prompt_cache_hit_tokens` / `prompt_cache_miss_tokens` are observable.
- Temperature has no effect with `-thinking`: the model enables thinking by default,
  so the agent explicitly disables it, otherwise `-temperature` would be a no-op.
