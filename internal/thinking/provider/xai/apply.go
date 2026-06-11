// Package xai implements thinking configuration for xAI Grok Responses API models.
//
// xAI models use the OpenAI Responses API compatible reasoning.effort format
// with discrete levels.
package xai

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/codex"
)

// Applier implements thinking.ProviderApplier for xAI models.
type Applier struct {
	codex.Applier
}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new xAI thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("xai", NewApplier())
}
