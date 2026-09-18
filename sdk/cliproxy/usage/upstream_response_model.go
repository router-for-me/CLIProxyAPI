package usage

import (
	"context"
	"strings"
)

// ExtractionFamily selects protocol-specific model extraction rules for one attempt.
type ExtractionFamily string

const (
	FamilyOpenAI ExtractionFamily = "openai"
	FamilyClaude ExtractionFamily = "claude"
	FamilyGemini ExtractionFamily = "gemini"
)

const maxUpstreamResponseModelRunes = 200

type upstreamResponseModelContextKey struct{}

type upstreamResponseModelObserver struct {
	first     string
	selected  string
	eventType string
	family    ExtractionFamily
}

// BeginUpstreamResponseModelObservation attaches a fresh observer to ctx.
func BeginUpstreamResponseModelObservation(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, upstreamResponseModelContextKey{}, &upstreamResponseModelObserver{})
}

func observerFromContext(ctx context.Context) *upstreamResponseModelObserver {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(upstreamResponseModelContextKey{}).(*upstreamResponseModelObserver)
	return observer
}

// ObserveUpstreamResponseModel records a candidate model for the current attempt.
func ObserveUpstreamResponseModel(ctx context.Context, model string, terminal bool) {
	observer := observerFromContext(ctx)
	if observer == nil {
		return
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	runes := []rune(model)
	if len(runes) > maxUpstreamResponseModelRunes {
		model = string(runes[:maxUpstreamResponseModelRunes])
	}
	if terminal {
		observer.selected = model
		return
	}
	if observer.first == "" {
		observer.first = model
	}
}

// RememberUpstreamResponseEventType stores the latest SSE event name for this attempt.
func RememberUpstreamResponseEventType(ctx context.Context, eventType string) {
	observer := observerFromContext(ctx)
	if observer == nil {
		return
	}
	observer.eventType = strings.TrimSpace(eventType)
}

// LastUpstreamResponseEventType returns the latest remembered SSE event name.
func LastUpstreamResponseEventType(ctx context.Context) string {
	observer := observerFromContext(ctx)
	if observer == nil {
		return ""
	}
	return observer.eventType
}

// UpstreamResponseModelFromContext returns the selected or first observed model.
func UpstreamResponseModelFromContext(ctx context.Context) string {
	observer := observerFromContext(ctx)
	if observer == nil {
		return ""
	}
	if observer.selected != "" {
		return observer.selected
	}
	return observer.first
}

// BindUpstreamResponseModelFamily binds the extraction family for this attempt.
func BindUpstreamResponseModelFamily(ctx context.Context, family ExtractionFamily) {
	observer := observerFromContext(ctx)
	if observer == nil {
		return
	}
	observer.family = family
}

// UpstreamResponseModelFamilyFromContext returns the bound family and whether one is set.
func UpstreamResponseModelFamilyFromContext(ctx context.Context) (ExtractionFamily, bool) {
	observer := observerFromContext(ctx)
	if observer == nil || observer.family == "" {
		return "", false
	}
	return observer.family, true
}

// MapExecutorToExtractionFamily maps provider/executor identifiers onto one family.
func MapExecutorToExtractionFamily(provider, executorType string) ExtractionFamily {
	ident := strings.ToLower(provider + "\x00" + executorType)
	if strings.Contains(ident, "claude") {
		return FamilyClaude
	}
	if strings.Contains(ident, "gemini") ||
		strings.Contains(ident, "antigravity") ||
		strings.Contains(ident, "aistudio") ||
		strings.Contains(ident, "vertex") {
		return FamilyGemini
	}
	return FamilyOpenAI
}
