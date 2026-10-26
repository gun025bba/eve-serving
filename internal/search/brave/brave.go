// Package brave is a placeholder for the Brave Search API provider. It is
// registered so it can be listed in search.providers, but it is disabled
// (ErrDisabled) until implemented and an API key file is configured; signing
// up for Brave needs an account and payment method (operator approval).
package brave

import (
	"context"
	"errors"

	"github.com/gun025bba/llm-serving/internal/search"
)

func init() { search.Register("brave", New) }

func New(cfg search.Config) (search.Provider, error) {
	if cfg.BraveKeyFile == "" {
		return nil, search.ErrDisabled
	}
	return nil, errors.New("brave provider not implemented yet")
}

// Provider exists for interface completeness.
type Provider struct{}

func (Provider) Name() string { return "brave" }
func (Provider) Search(context.Context, search.Query) ([]search.Result, error) {
	return nil, search.ErrDisabled
}
