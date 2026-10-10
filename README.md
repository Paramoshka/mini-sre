# mini-sre

A minimal SRE agent for DeepSeek (OpenAI-compatible API): a CLI chat with streaming,
history, and function calling.

## Features

- REPL and one-shot mode, the answer is printed as it is generated;
- read-only tools for load average, disk space, systemd/Docker status and logs;
- the same probes run locally or over SSH on hosts configured in YAML;
- a Telegram bot for personal messages and forwarded text from allowed users;
- token and DeepSeek server cache stats after every answer;
- DeepSeek thinking mode (disabled by default).

## Requirements

- Go 1.25+;
- Linux with `/proc/loadavg` and GNU coreutils (`cat`, `df`);
- a DeepSeek API key.

Systemd probes need `systemctl` and `journalctl`; Docker probes need the Docker
CLI and access to its daemon. These commands must be available in the SSH user's
noninteractive PATH on remote hosts. Commands run as the current local or SSH
user; the agent does not invoke `sudo`.

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
| `-config` | empty | YAML file with SSH hosts and Telegram allowed user IDs; empty means local only |
| `-telegram` | `false` | run the bot instead of the console; requires `-config` |

The key is read from the `DEEPSEEK_API_KEY` environment variable, never from
arguments. On startup the CLI loads `.env` from the current directory if present;
real environment variables take precedence over the file. A malformed `.env`
aborts startup.

## Hosts and SSH

Copy `hosts.example.yaml` to `hosts.yaml` and replace the example hosts and
credentials. The latter is ignored by Git. Keep the config and private keys
readable only by their owner (`chmod 600 hosts.yaml keys/sre_ed25519`). If you use
another config filename, keep that file outside Git too.

```bash
go run ./cmd -config hosts.yaml "сколько места на web-1 в /var?"
go run ./cmd -config hosts.yaml
```

`local` always means the machine running this process; it is available without
configuration and cannot be redefined. Other IDs come from the `hosts` mapping.
Each host needs `address`, `user` and exactly one of `key_file` or `password`.
The default port is 22. Passwords are literal YAML strings, not environment
references. Keys with a passphrase are not supported in this MVP.

Relative `key_file` and `known_hosts` paths are resolved against the YAML file's
directory. Omit `known_hosts` to use the current user's `~/.ssh/known_hosts`.
The file must contain the server's independently verified host key; unknown or
changed host keys are rejected. The agent does not add keys automatically.

Configuration is loaded once at startup. Key files and `known_hosts` are read
when making a connection, so one unavailable remote host does not disable local
checks. YAML syntax, unknown fields, invalid ports and conflicting credentials
are rejected at startup. Host IDs are exposed to the model; addresses, passwords,
key paths and key contents are not included in the host listing or prompts.

## Diagnostic tools

All probes except `list_hosts` accept `host_id`, defaulting to `local`.

| Tool | Other arguments | Behavior |
|---|---|---|
| `list_hosts` | none | list configured IDs and `local` |
| `get_load_average` | none | load for 1, 5, 15 minutes and process counts |
| `get_disk_usage` | `path`, default `/` | total, used, available bytes formatted as human-readable sizes |
| `get_service_status` | required `service`; `backend`, default `systemd`; `scope`, default `system` | systemd unit state or Docker container `.State` |
| `get_service_logs` | `service`, `backend`, `scope`, `lines`, `since_minutes` | recent journal or container logs |

`backend` is `systemd` or `docker`, selected per request. Systemd names may omit
the `.service` suffix; Docker targets are container names or IDs, not Compose or
Swarm service names. An inactive/failed unit or exited container is a valid
status result. Missing services, unavailable commands/daemons and permission
errors are reported explicitly. For systemd logs, omit `service` to read the
general journal. Docker logs require a container.

For systemd, `scope` is `system` (default) or `user`. User scope selects the
service manager and journal of the local process user or the configured SSH
user. It requires that user's systemd manager to be available. To inspect the
agent installed with the example user unit, use
`{"host_id":"local","service":"mini-sre","scope":"user"}` with either service
tool. Omit `service` with `scope=user` to read that user's general journal.
Docker does not accept `scope`.

Logs default to the last 100 records from the last 60 minutes. `lines` must be
1..500 and `since_minutes` must be positive. Both stdout and stderr logs are
included. There is no follow mode. Journal access depends on the selected user's
permissions; a restricted journal can contain fewer records than the full
system journal.

Each command has a 15-second timeout, including SSH connection and handshake.
Captured stdout and stderr together are limited to 64 KiB, with an explicit
truncation marker. Local arguments are passed directly to the process; SSH
arguments are quoted for the remote shell. The model cannot supply a program or
arbitrary shell command. Cancelling SSH closes the connection, but does not
guarantee that the remote process has exited. Logs can contain application
secrets; review which hosts/users the agent may access before sending them to
the model.

## Telegram

Create a bot, set its token and put your numeric Telegram user ID in
`telegram.allowed_user_ids` in the YAML file (not your username). Incoming Bot
API messages carry this ID in `message.from.id`.

```bash
export TELEGRAM_BOT_TOKEN='your-bot-token'
go run ./cmd -config hosts.yaml -telegram
```

`DEEPSEEK_API_KEY` is also required. Both tokens may be stored in the ignored
`.env` file; use owner-only permissions for it. The bot uses outbound long
polling, so the nettop needs access to Telegram and DeepSeek, without a public
HTTP endpoint. A pre-existing webhook causes a clear startup error and is not
removed automatically. Run only one polling process per bot token.

The bot accepts text and forwarded text in personal chats from allowed users.
Other senders, groups and non-text messages are ignored before calling the model.
Use `/start` for help and `/clear` to reset the chat. Requests are processed
sequentially; histories are separate per chat and retain the last 20 completed
requests, including their tool calls. Histories and the local polling offset are
not persisted across restarts. Telegram may redeliver an update if the process
stops before acknowledging it.

Telegram receives final answers with fenced code blocks, inline code and
`**bold**` text converted to native Telegram entities. Command contents are
preserved literally. Other Markdown syntax remains plain text; incomplete
fenced blocks are kept literally. Replies are split into messages of at most
4000 UTF-16 units and 100 entities, preferably at line boundaries, retaining
formatting in each part. Streaming, reasoning output and token statistics remain console
features. Network polling failures retry with a delay capped at 30 seconds;
Telegram's longer `retry_after` is respected. Replies use at most three delivery
attempts; delivery failures are logged and do not rerun the model. A network
failure after Telegram accepted a reply can result in a duplicate reply.

CLI and Telegram are independent processes sharing the same configuration and
probe code, not the same conversation history. Send SIGINT/SIGTERM to stop the
bot and cancel active requests.

For continuous operation, `examples/mini-sre.service` is a **user** systemd unit
expecting the binary, `.env` and `hosts.yaml` in `~/mini-sre`. Adjust its paths if
you keep the project elsewhere. Build the binary using the command above, then:

```bash
mkdir -p ~/.config/systemd/user
install -m 644 examples/mini-sre.service ~/.config/systemd/user/mini-sre.service
systemctl --user daemon-reload
systemctl --user enable --now mini-sre.service
journalctl --user -u mini-sre.service
```

For a user service to run without a login session, enable lingering for that
account using `loginctl enable-linger USER` if your system permits it.

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
- `internal/cli` - REPL, one-shot, streaming console output;
- `internal/session` - shared history and agent loop (up to 5 tool call rounds, then a final answer without tools);
- `internal/tools` - probes and their JSON schemas for the model;
  `load.go`, `disk.go`, `service.go` contain the individual probes;
- `internal/config`, `internal/remote` - YAML hosts and local/SSH command execution;
- `internal/telegram` - Bot API polling, authorization and chat sessions;
- `cmd/main.go` - flags and wiring.

## Notes

- DeepSeek context caching is server-side and automatic, there is no API setting; only
  `prompt_cache_hit_tokens` / `prompt_cache_miss_tokens` are observable.
- Temperature has no effect with `-thinking`: the model enables thinking by default,
  so the agent explicitly disables it, otherwise `-temperature` would be a no-op.
