package codearts

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("codearts", NewApplier())
}

func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	if modelInfo == nil || modelInfo.Thinking == nil {
		return body, nil
	}
	return body, nil
}
