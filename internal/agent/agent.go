// Package agent openai compatable
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/respjson"
)

type AgentClient struct {
	cfg    Config
	client openai.Client
}

func New(cfg Config) (*AgentClient, error) {
	cfg = cfg.withDefaults()

	if cfg.APIKey == "" {
		return nil, errors.New("agent: api key is required")
	}
	if *cfg.MaxRetries < 0 {
		return nil, errors.New("agent: max retries must be >= 0")
	}

	client := openai.NewClient(
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithRequestTimeout(cfg.Timeout),
		option.WithMaxRetries(*cfg.MaxRetries),
	)

	return &AgentClient{cfg: cfg, client: client}, nil
}

func (c *AgentClient) Chat(ctx context.Context, req Request) (*Response, error) {
	params, err := c.buildParams(req)
	if err != nil {
		return nil, err
	}

	completion, err := c.client.Chat.Completions.New(ctx, params, c.thinkingOption())
	if err != nil {
		return nil, fmt.Errorf("agent: chat: %w", err)
	}
	if len(completion.Choices) == 0 {
		return nil, errors.New("agent: chat: response has no choices")
	}

	choice := completion.Choices[0]
	return &Response{
		ID:               completion.ID,
		Model:            completion.Model,
		Content:          choice.Message.Content,
		ReasoningContent: extraString(choice.Message.JSON.ExtraFields, "reasoning_content"),
		FinishReason:     choice.FinishReason,
		Usage:            usageFrom(completion.Usage),
	}, nil
}

func (c *AgentClient) buildParams(req Request) (openai.ChatCompletionNewParams, error) {
	messages, err := messageParams(req.Messages)
	if err != nil {
		return openai.ChatCompletionNewParams{}, err
	}

	params := openai.ChatCompletionNewParams{
		Model:    c.cfg.Model,
		Messages: messages,
	}

	temperature := c.cfg.Temperature
	if req.Temperature != nil {
		temperature = req.Temperature
	}
	if temperature != nil {
		params.Temperature = openai.Float(*temperature)
	}

	maxTokens := c.cfg.MaxTokens
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}
	if maxTokens > 0 {
		params.MaxTokens = openai.Int(int64(maxTokens))
	}

	return params, nil
}

func (c *AgentClient) thinkingOption() option.RequestOption {
	return option.WithJSONSet("thinking", map[string]string{"type": string(c.cfg.Thinking)})
}

func usageFrom(raw openai.CompletionUsage) Usage {
	return Usage{
		PromptTokens:     raw.PromptTokens,
		CompletionTokens: raw.CompletionTokens,
		TotalTokens:      raw.TotalTokens,
		CacheHitTokens:   extraInt64(raw.JSON.ExtraFields, "prompt_cache_hit_tokens"),
		CacheMissTokens:  extraInt64(raw.JSON.ExtraFields, "prompt_cache_miss_tokens"),
		ReasoningTokens:  raw.CompletionTokensDetails.ReasoningTokens,
	}
}

func messageParams(messages []Message) ([]openai.ChatCompletionMessageParamUnion, error) {
	if len(messages) == 0 {
		return nil, errors.New("agent: chat: messages are required")
	}

	params := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))
	for i, m := range messages {
		switch m.Role {
		case RoleSystem:
			params = append(params, openai.SystemMessage(m.Content))
		case RoleUser:
			params = append(params, openai.UserMessage(m.Content))
		case RoleAssistant:
			params = append(params, openai.AssistantMessage(m.Content))
		default:
			return nil, fmt.Errorf("agent: chat: message %d has unknown role %q", i, m.Role)
		}
	}
	return params, nil
}

func extraInt64(fields map[string]respjson.Field, key string) int64 {
	field, ok := fields[key]
	if !ok || field.Raw() == "" || field.Raw() == respjson.Null {
		return 0
	}
	var value int64
	if err := json.Unmarshal([]byte(field.Raw()), &value); err != nil {
		return 0
	}
	return value
}

func extraString(fields map[string]respjson.Field, key string) string {
	field, ok := fields[key]
	if !ok || field.Raw() == "" || field.Raw() == respjson.Null {
		return ""
	}
	var value string
	if err := json.Unmarshal([]byte(field.Raw()), &value); err != nil {
		return ""
	}
	return value
}
