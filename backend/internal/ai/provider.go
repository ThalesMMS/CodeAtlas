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
	Embed(ctx context.Context, texts []string) ([][]float64, error)
}

type Disabled struct{}

func (Disabled) Name() string    { return "unconfigured-llm" }
func (Disabled) Available() bool { return false }
func (Disabled) Complete(context.Context, string, string, int) (string, error) {
	return "", ErrUnavailable
}
func (Disabled) Embed(context.Context, []string) ([][]float64, error) {
	return nil, ErrUnavailable
}
