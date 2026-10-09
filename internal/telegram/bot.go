package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"mini-sre/internal/session"
)

type Bot struct {
	token      string
	allowed    map[int64]bool
	newSession func() *session.Session
	sessions   map[int64]*session.Session
	errOut     io.Writer
	client     *http.Client
	baseURL    string
	offset     int64
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

func New(token string, users []int64, newSession func() *session.Session, errOut io.Writer) (*Bot, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("telegram: TELEGRAM_BOT_TOKEN is required")
	}
	if len(users) == 0 {
		return nil, errors.New("telegram: allowed_user_ids must not be empty")
	}
	allowed := make(map[int64]bool, len(users))
	for _, id := range users {
		if id <= 0 {
			return nil, errors.New("telegram: allowed_user_ids must be positive")
		}
		allowed[id] = true
	}
	return &Bot{
		token: token, allowed: allowed, newSession: newSession,
		sessions: make(map[int64]*session.Session), errOut: errOut,
		client: &http.Client{Timeout: 45 * time.Second}, baseURL: "https://api.telegram.org",
	}, nil
}

func (b *Bot) Run(ctx context.Context) error {
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
	delay := time.Second
	for ctx.Err() == nil {
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
			if err := b.handle(ctx, u.Message); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				var apiErr *apiError
				if errors.As(err, &apiErr) && apiErr.Code == http.StatusUnauthorized {
					return err
				}
				b.log(err)
			}
			b.offset = u.ID + 1
		}
	}
	return nil
}

func (b *Bot) handle(ctx context.Context, m *message) error {
	if m == nil || m.From == nil || m.Chat.Type != "private" || !b.allowed[m.From.ID] || strings.TrimSpace(m.Text) == "" {
		return nil
	}
	input := strings.TrimSpace(m.Text)
	switch input {
	case "/start":
		return b.send(ctx, m.Chat.ID, "Пришли запрос или перешли сообщение о проблеме. Укажи сервер и сервис; local — машина агента. /clear очищает историю.")
	case "/clear":
		if s := b.sessions[m.Chat.ID]; s != nil {
			s.Reset()
		}
		return b.send(ctx, m.Chat.ID, "История очищена.")
	}
	if strings.HasPrefix(input, "/") {
		return b.send(ctx, m.Chat.ID, "Неизвестная команда. /start — помощь, /clear — очистить историю.")
	}
	s := b.sessions[m.Chat.ID]
	if s == nil {
		s = b.newSession()
		s.MaxHistoryTurns = 20
		b.sessions[m.Chat.ID] = s
	}
	resp, err := s.Ask(ctx, input)
	if err != nil {
		b.log(err)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return b.send(ctx, m.Chat.ID, "Не удалось выполнить запрос. Подробности — в журнале агента.")
	}
	text := resp.Content
	if strings.TrimSpace(text) == "" {
		text = "Модель вернула пустой ответ. Попробуй уточнить запрос."
	}
	return b.send(ctx, m.Chat.ID, text)
}

func (b *Bot) send(ctx context.Context, chatID int64, text string) error {
	for _, part := range splitText(text) {
		if err := b.callWithRetry(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": part}, nil); err != nil {
			return err
		}
	}
	return nil
}

func splitText(text string) []string {
	var parts []string
	start, units := 0, 0
	for i, r := range text {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if units+n > 4000 {
			parts = append(parts, text[start:i])
			start, units = i, 0
		}
		units += n
	}
	if start < len(text) {
		parts = append(parts, text[start:])
	}
	return parts
}

func (b *Bot) log(err error) {
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
