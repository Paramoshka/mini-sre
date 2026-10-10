package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"mini-sre/internal/session"
)

const maxHistoryTurns = 20

type Bot struct {
	token         string
	users         func() []int64
	newSession    func() *session.Session
	sessions      map[int64]*session.Session
	errOut        io.Writer
	client        *http.Client
	baseURL       string
	offset        int64
	statePath     string
	offsetSavedAt time.Time
	tasks         *taskScheduler
	logMu         sync.Mutex
}

type update struct {
	ID      int64    `json:"update_id"`
	Message *message `json:"message"`
}

type message struct {
	Text string `json:"text"`
	From *struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
}

func New(token string, users func() []int64, newSession func() *session.Session, errOut io.Writer) (*Bot, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("telegram: TELEGRAM_BOT_TOKEN is required")
	}
	if users == nil {
		return nil, errors.New("telegram: allowed_user_ids must not be empty")
	}
	initialUsers := users()
	if len(initialUsers) == 0 {
		return nil, errors.New("telegram: allowed_user_ids must not be empty")
	}
	for _, id := range initialUsers {
		if id <= 0 {
			return nil, errors.New("telegram: allowed_user_ids must be positive")
		}
	}
	return &Bot{
		token: token, users: users, newSession: newSession,
		sessions: make(map[int64]*session.Session), errOut: errOut,
		client: &http.Client{Timeout: 45 * time.Second}, baseURL: "https://api.telegram.org",
	}, nil
}

func (b *Bot) Run(ctx context.Context) (runErr error) {
	var webhook struct {
		URL string `json:"url"`
	}
	if err := b.callWithRetry(ctx, "getWebhookInfo", struct{}{}, &webhook); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if webhook.URL != "" {
		return errors.New("telegram: webhook is configured; remove it manually before using polling")
	}
	if b.tasks != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- b.tasks.run(ctx)
			cancel()
		}()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				runErr = err
			}
		}()
	}
	delay := time.Second
	for ctx.Err() == nil {
		// Telegram expires queued updates after 24h and can later reuse lower IDs.
		if b.offset != 0 && time.Since(b.offsetSavedAt) >= 24*time.Hour {
			b.offset = 0
		}
		var updates []update
		err := b.call(ctx, "getUpdates", map[string]any{
			"offset": b.offset, "timeout": 30, "allowed_updates": []string{"message"},
		}, &updates)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if !retryable(err) {
				return err
			}
			b.log(err)
			if err := wait(ctx, retryDelay(err, delay)); err != nil {
				return nil
			}
			delay = min(delay*2, 30*time.Second)
			continue
		}
		delay = time.Second
		sort.Slice(updates, func(i, j int) bool { return updates[i].ID < updates[j].ID })
		for _, u := range updates {
			if ctx.Err() != nil {
				return nil
			}
			if u.ID < b.offset {
				continue
			}
			if err := b.process(ctx, u); err != nil {
				var stateErr *stateError
				if errors.As(err, &stateErr) {
					return err
				}
				if ctx.Err() != nil {
					return nil
				}
				var apiErr *apiError
				if errors.As(err, &apiErr) && apiErr.Code == http.StatusUnauthorized {
					return err
				}
				b.log(err)
			}
		}
	}
	return nil
}

func (b *Bot) process(ctx context.Context, u update) error {
	text, err := b.handle(ctx, u.Message, u.ID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	nextOffset, savedAt := u.ID+1, time.Now().UTC()
	if err := b.saveState(nextOffset, savedAt); err != nil {
		return err
	}
	b.offset, b.offsetSavedAt = nextOffset, savedAt
	if text == "" {
		return nil
	}
	return b.send(ctx, u.Message.Chat.ID, text)
}

func (b *Bot) handle(ctx context.Context, m *message, updateID int64) (string, error) {
	if m == nil || m.From == nil || m.Chat.Type != "private" || !slices.Contains(b.users(), m.From.ID) || strings.TrimSpace(m.Text) == "" {
		return "", nil
	}
	input := strings.TrimSpace(m.Text)
	if input == "/task" || strings.HasPrefix(input, "/task ") || strings.HasPrefix(input, "/task\n") || strings.HasPrefix(input, "/task\t") {
		if b.tasks == nil {
			return "Scheduled tasks are not enabled for this process.", nil
		}
		return b.tasks.command(ctx, m, updateID, input)
	}
	switch input {
	case "/start":
		return "Send a request or forward a problem report. Specify the host and service; local is the agent machine. /clear resets history. /task manages scheduled tasks.", nil
	case "/clear":
		if s := b.sessions[m.Chat.ID]; s != nil {
			s.Reset()
		}
		return "History cleared.", nil
	}
	if strings.HasPrefix(input, "/") {
		return "Unknown command. /start — help, /clear — reset history.", nil
	}
	s := b.sessions[m.Chat.ID]
	if s == nil {
		s = b.newSession()
		s.MaxHistoryTurns = maxHistoryTurns
		b.sessions[m.Chat.ID] = s
	}
	resp, err := s.Ask(ctx, input)
	if err != nil {
		b.log(err)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "Could not complete the request. See the agent log for details.", nil
	}
	text := resp.Content
	if strings.TrimSpace(text) == "" {
		text = "The model returned an empty response. Try a more specific request."
	}
	return text, nil
}

func (b *Bot) send(ctx context.Context, chatID int64, text string) error {
	for _, part := range formatReply(text) {
		params := map[string]any{"chat_id": chatID, "text": part.Text}
		if len(part.Entities) > 0 {
			params["entities"] = part.Entities
		}
		if err := b.callWithRetry(ctx, "sendMessage", params, nil); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bot) log(err error) {
	b.logMu.Lock()
	defer b.logMu.Unlock()
	if b.errOut != nil {
		fmt.Fprintln(b.errOut, strings.ReplaceAll(err.Error(), b.token, "[redacted]"))
	}
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
