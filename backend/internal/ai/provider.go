package ai

import (
	"context"
	"errors"
)

var ErrUnavailable = errors.New("LLM provider is not configured")

type Provider interface {
	Name() string
	Available() bool
	Complete(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, error)
}

type Disabled struct{}

func (Disabled) Name() string    { return "unconfigured-llm" }
func (Disabled) Available() bool { return false }
func (Disabled) Complete(context.Context, string, string, int) (string, error) {
	return "", ErrUnavailable
}
