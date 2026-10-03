// Package agent openai compatable
package agent

import (
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

type Effort string

type AgentClient struct {
	Effort
	Client openai.Client
}

type AgentOptions struct {
	APIKey string
}

func (a *AgentClient) New(options AgentOptions) *AgentClient {

	openaiClient := openai.NewClient(option.WithAPIKey(options.APIKey))

	agentClinet := AgentClient{
		Client: openaiClient,
	}

	return &agentClinet
}
