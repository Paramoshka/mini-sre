package agent

import (
	"os"
	"time"
)

const (
	DefaultBaseURL = "https://api.deepseek.com"
	DefaultModel   = "deepseek-flash"
	DefaultTimeout = 60 * time.Second
)

type ThinkingMode string

const (
	ThinkingEnabled  ThinkingMode = "enabled"
	ThinkingDisabled ThinkingMode = "disabled"
)

type Config struct {
	APIKey      string
	BaseURL     string
	Model       string
	Temperature *float64
	MaxTokens   int
	Thinking    ThinkingMode
	Timeout     time.Duration
}

func (c Config) withDefaults() Config {
	if c.APIKey == "" {
		c.APIKey = os.Getenv("DEEPSEEK_API_KEY")
	}
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.Thinking == "" {
		c.Thinking = ThinkingDisabled
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	return c
}

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role    Role
	Content string
}

type Request struct {
	Messages    []Message
	Temperature *float64
	MaxTokens   *int
}

type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CacheHitTokens   int64
	CacheMissTokens  int64
	ReasoningTokens  int64
}

type Response struct {
	ID               string
	Model            string
	Content          string
	ReasoningContent string
	FinishReason     string
	Usage            Usage
}
