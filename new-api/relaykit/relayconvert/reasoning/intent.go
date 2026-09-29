package reasoning

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
)

type Effort string

const (
	EffortNone    Effort = "none"
	EffortMinimal Effort = "minimal"
	EffortLow     Effort = "low"
	EffortMedium  Effort = "medium"
	EffortHigh    Effort = "high"
	EffortXHigh   Effort = "xhigh"
	EffortMax     Effort = "max"
)

type Mode string

type Source string

// ClientError marks invalid user-supplied reasoning controls so host handlers
// can return a 4xx without classifying unrelated adapter failures as client
// errors.
type ClientError struct {
	err error
}

func (e *ClientError) Error() string { return e.err.Error() }
func (e *ClientError) Unwrap() error { return e.err }

func AsClientError(err error) error {
	if err == nil {
		return nil
	}
	var clientErr *ClientError
	if errors.As(err, &clientErr) {
		return err
	}
	return &ClientError{err: err}
}

func IsClientError(err error) bool {
	var clientErr *ClientError
	return errors.As(err, &clientErr)
}

const (
	ModeUnset    Mode = ""
	ModeEnabled  Mode = "enabled"
	ModeAdaptive Mode = "adaptive"
	ModeDisabled Mode = "disabled"
)

const (
	SourceExplicit Source = "explicit"
	SourceNative   Source = "native"
	SourceSuffix   Source = "suffix"
	SourcePivot    Source = "pivot"
)

var (
	ErrEffortConflict      = errors.New("reasoning settings conflict")
	ErrUnsupportedEffort   = errors.New("unsupported reasoning effort")
	ErrThinkingNotDisabled = errors.New("thinking cannot be disabled")
)

// Intent is the protocol-independent part of a request's reasoning controls.
// Summary visibility is intentionally independent from reasoning strength.
type Intent struct {
	Mode            Mode
	Effort          Effort
	BudgetTokens    *int
	IncludeThoughts *bool
	Source          Source
	BudgetSource    Source
}

func (i Intent) HasStrength() bool {
	return i.Mode != ModeUnset || i.Effort != "" || i.BudgetTokens != nil
}

func (i Intent) IsEmpty() bool {
	return !i.HasStrength() && i.IncludeThoughts == nil
}

// IntentFromState reconstructs a portable intent from host- or pivot-carried
// conversion state. A nil state is an empty intent.
func IntentFromState(state *dto.ReasoningConversionState) Intent {
	if state == nil {
		return Intent{}
	}
	return Intent{
		Mode:            Mode(state.Mode),
		Effort:          Effort(state.Effort),
		BudgetTokens:    state.BudgetTokens,
		IncludeThoughts: state.IncludeThoughts,
		Source:          SourceSuffix,
		BudgetSource:    SourceSuffix,
	}
}

// StateFromIntent copies the portable fields of an intent into conversion
// state. Empty intents produce nil so callers can omit the field.
func StateFromIntent(intent Intent) *dto.ReasoningConversionState {
	if intent.IsEmpty() {
		return nil
	}
	return &dto.ReasoningConversionState{
		Mode:            string(intent.Mode),
		Effort:          string(intent.Effort),
		BudgetTokens:    intent.BudgetTokens,
		IncludeThoughts: intent.IncludeThoughts,
	}
}

func ParseEffort(value string) (Effort, error) {
	effort := Effort(strings.ToLower(strings.TrimSpace(value)))
	if effort == "" {
		return "", nil
	}
	switch effort {
	case EffortNone, EffortMinimal, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax:
		return effort, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedEffort, value)
	}
}

// reasoningDiagnostic describes a best-effort resolution between reasoning
// controls that is not tied to a single provider's field layout. The target
// format is filled in by the conversion pipeline.
func reasoningDiagnostic(code string, message string) types.ConversionDiagnostic {
	return types.ConversionDiagnostic{
		Code:     code,
		Path:     "reasoning",
		Message:  message,
		Severity: types.ConversionDiagnosticWarning,
	}
}

// normalizeIntent canonicalizes one intent. Only unknown effort or mode values
// are errors; contradictions between mode, effort, and budget are resolved in
// favor of the more specific control (a numeric budget beats a mode or effort,
// effort none beats an enabling mode) and reported as diagnostics.
func normalizeIntent(intent Intent) (Intent, []types.ConversionDiagnostic, error) {
	effort, err := ParseEffort(string(intent.Effort))
	if err != nil {
		return Intent{}, nil, err
	}
	intent.Effort = effort

	switch intent.Mode {
	case ModeUnset, ModeEnabled, ModeAdaptive, ModeDisabled:
	default:
		return Intent{}, nil, fmt.Errorf("unsupported reasoning mode %q", intent.Mode)
	}

	var diagnostics []types.ConversionDiagnostic
	if intent.BudgetTokens != nil {
		budget := *intent.BudgetTokens
		if budget < -1 {
			diagnostics = append(diagnostics, reasoningDiagnostic(
				"reasoning_budget_adjusted",
				fmt.Sprintf("thinking budget %d is below -1; using the dynamic budget -1", budget),
			))
			budget = -1
			intent.BudgetTokens = &budget
		}
		if budget == 0 {
			if intent.Mode == ModeEnabled || intent.Mode == ModeAdaptive || (intent.Effort != "" && intent.Effort != EffortNone) {
				diagnostics = append(diagnostics, reasoningDiagnostic(
					"explicit_fields_conflict",
					"a zero thinking budget disables thinking; the enabling mode or effort was ignored",
				))
			}
			intent.Mode = ModeDisabled
			intent.Effort = EffortNone
		} else if intent.Mode == ModeDisabled || intent.Effort == EffortNone {
			diagnostics = append(diagnostics, reasoningDiagnostic(
				"explicit_fields_conflict",
				fmt.Sprintf("a non-zero thinking budget %d enables thinking; the disabling mode or effort was ignored", budget),
			))
			intent.Mode = ModeEnabled
			intent.Effort = ""
		} else if intent.Mode == ModeUnset {
			intent.Mode = ModeEnabled
		}
	}

	if intent.Effort == EffortNone {
		if intent.Mode == ModeEnabled || intent.Mode == ModeAdaptive {
			diagnostics = append(diagnostics, reasoningDiagnostic(
				"explicit_fields_conflict",
				"effort none disables thinking; the enabling mode was ignored",
			))
		}
		intent.Mode = ModeDisabled
	}

	return intent, diagnostics, nil
}

// MergeExplicitAndSuffix combines structured request fields with a model-name
// alias or @ modifier. The model name wins wherever the two disagree: it is
// also the billing identity, so upstream behavior and accounting must follow
// it. The alias only overrides what it states explicitly; a bare enable such
// as -thinking keeps the strength supplied by request fields. Every override
// is reported as a suffix_overrode_request diagnostic.
func MergeExplicitAndSuffix(explicit Intent, suffix Intent, model string) (Intent, []types.ConversionDiagnostic, error) {
	explicit, diagnostics, err := normalizeIntent(explicit)
	if err != nil {
		return Intent{}, nil, err
	}
	suffix, suffixDiagnostics, err := normalizeIntent(suffix)
	if err != nil {
		return Intent{}, nil, err
	}
	diagnostics = append(diagnostics, suffixDiagnostics...)

	if !explicit.HasStrength() {
		if explicit.IncludeThoughts != nil {
			suffix.IncludeThoughts = explicit.IncludeThoughts
		}
		return suffix, diagnostics, nil
	}
	if !suffix.HasStrength() {
		if explicit.IncludeThoughts == nil {
			explicit.IncludeThoughts = suffix.IncludeThoughts
		}
		return explicit, diagnostics, nil
	}

	merged := suffix
	if explicit.IncludeThoughts != nil {
		merged.IncludeThoughts = explicit.IncludeThoughts
	}
	explicitDisabled := explicit.Mode == ModeDisabled || explicit.Effort == EffortNone
	suffixDisabled := suffix.Mode == ModeDisabled || suffix.Effort == EffortNone
	if explicitDisabled != suffixDisabled {
		diagnostics = append(diagnostics, reasoningDiagnostic(
			"suffix_overrode_request",
			fmt.Sprintf("model %q: the model name and request fields disagree about whether thinking is enabled; the model name wins", model),
		))
		return merged, diagnostics, nil
	}
	if suffixDisabled {
		return merged, diagnostics, nil
	}

	if merged.Mode == ModeEnabled && explicit.Mode == ModeAdaptive {
		merged.Mode = ModeAdaptive
	}
	if suffix.Effort == "" && suffix.BudgetTokens == nil {
		// A bare enable says nothing about strength; request fields supply it.
		merged.Effort = explicit.Effort
		merged.BudgetTokens = explicit.BudgetTokens
		merged.BudgetSource = explicit.BudgetSource
		return merged, diagnostics, nil
	}
	if explicit.Effort != "" && explicit.Effort != suffix.Effort {
		diagnostics = append(diagnostics, reasoningDiagnostic(
			"suffix_overrode_request",
			fmt.Sprintf("model %q: request effort %q was replaced by the reasoning strength in the model name", model, explicit.Effort),
		))
	}
	if explicit.BudgetTokens != nil && (suffix.BudgetTokens == nil || *explicit.BudgetTokens != *suffix.BudgetTokens) {
		diagnostics = append(diagnostics, reasoningDiagnostic(
			"suffix_overrode_request",
			fmt.Sprintf("model %q: request thinking budget %d was replaced by the reasoning strength in the model name", model, *explicit.BudgetTokens),
		))
	}
	return merged, diagnostics, nil
}

// MergeExplicit combines two structured representations of the same request.
// primary wins wherever they disagree; callers pass the more specific
// representation first. A numeric budget and an effort may coexist: Claude and
// OpenRouter expose both controls, and keeping both is what lets an in-memory
// OpenAI pivot preserve an exact budget for budget-based targets while
// retaining an effort for level-based targets. Disagreements are reported as
// explicit_fields_conflict diagnostics.
func MergeExplicit(primary Intent, secondary Intent, model string) (Intent, []types.ConversionDiagnostic, error) {
	primary, diagnostics, err := normalizeIntent(primary)
	if err != nil {
		return Intent{}, nil, err
	}
	secondary, secondaryDiagnostics, err := normalizeIntent(secondary)
	if err != nil {
		return Intent{}, nil, err
	}
	diagnostics = append(diagnostics, secondaryDiagnostics...)

	if primary.IsEmpty() {
		return secondary, diagnostics, nil
	}
	if secondary.IsEmpty() {
		return primary, diagnostics, nil
	}

	primaryDisabled := primary.Mode == ModeDisabled || primary.Effort == EffortNone
	secondaryDisabled := secondary.Mode == ModeDisabled || secondary.Effort == EffortNone
	if primary.HasStrength() && secondary.HasStrength() && primaryDisabled != secondaryDisabled {
		diagnostics = append(diagnostics, reasoningDiagnostic(
			"explicit_fields_conflict",
			fmt.Sprintf("model %q: reasoning fields disagree about whether thinking is enabled; keeping the more specific field", model),
		))
		if primary.IncludeThoughts == nil {
			primary.IncludeThoughts = secondary.IncludeThoughts
		}
		return primary, diagnostics, nil
	}
	if primary.Effort != "" && secondary.Effort != "" && primary.Effort != secondary.Effort {
		diagnostics = append(diagnostics, reasoningDiagnostic(
			"explicit_fields_conflict",
			fmt.Sprintf("model %q: reasoning efforts %q and %q differ; keeping %q", model, primary.Effort, secondary.Effort, primary.Effort),
		))
	}
	if primary.BudgetTokens != nil && secondary.BudgetTokens != nil && *primary.BudgetTokens != *secondary.BudgetTokens {
		diagnostics = append(diagnostics, reasoningDiagnostic(
			"explicit_fields_conflict",
			fmt.Sprintf("model %q: thinking budgets %d and %d differ; keeping %d", model, *primary.BudgetTokens, *secondary.BudgetTokens, *primary.BudgetTokens),
		))
	}

	merged := secondary
	if primary.Mode != ModeUnset {
		merged.Mode = primary.Mode
	}
	if primary.Effort != "" {
		merged.Effort = primary.Effort
	}
	if primary.BudgetTokens != nil {
		merged.BudgetTokens = primary.BudgetTokens
		merged.BudgetSource = primary.BudgetSource
	}
	if primary.IncludeThoughts != nil {
		merged.IncludeThoughts = primary.IncludeThoughts
	}
	merged, mergedDiagnostics, err := normalizeIntent(merged)
	if err != nil {
		return Intent{}, nil, err
	}
	return merged, append(diagnostics, mergedDiagnostics...), nil
}

type openRouterReasoning struct {
	Enabled   *bool  `json:"enabled,omitempty"`
	Effort    string `json:"effort,omitempty"`
	MaxTokens *int   `json:"max_tokens,omitempty"`
	Exclude   *bool  `json:"exclude,omitempty"`
}

func FromOpenAIChat(req *dto.GeneralOpenAIRequest) (Intent, []types.ConversionDiagnostic, error) {
	if req == nil {
		return Intent{}, nil, nil
	}

	var intent Intent
	intent.Source = SourceExplicit
	if req.ReasoningEffort != "" {
		effort, err := ParseEffort(req.ReasoningEffort)
		if err != nil {
			return Intent{}, nil, err
		}
		intent.Effort = effort
		if effort == EffortNone {
			intent.Mode = ModeDisabled
		} else {
			intent.Mode = ModeEnabled
		}
	}

	var diagnostics []types.ConversionDiagnostic
	if len(req.Reasoning) > 0 {
		var raw openRouterReasoning
		if err := kitutil.Unmarshal(req.Reasoning, &raw); err != nil {
			return Intent{}, nil, fmt.Errorf("invalid reasoning config: %w", err)
		}
		nested := Intent{BudgetTokens: raw.MaxTokens, Source: SourceExplicit, BudgetSource: SourceExplicit}
		if raw.Enabled != nil {
			if *raw.Enabled {
				nested.Mode = ModeEnabled
			} else {
				nested.Mode = ModeDisabled
				nested.Effort = EffortNone
			}
		}
		if raw.Effort != "" {
			effort, err := ParseEffort(raw.Effort)
			if err != nil {
				return Intent{}, nil, err
			}
			nested.Effort = effort
			if effort == EffortNone {
				nested.Mode = ModeDisabled
			} else if nested.Mode == ModeUnset {
				nested.Mode = ModeEnabled
			}
		}
		if raw.Exclude != nil {
			include := !*raw.Exclude
			nested.IncludeThoughts = &include
		}
		merged, mergeDiagnostics, err := MergeExplicit(intent, nested, req.Model)
		if err != nil {
			return Intent{}, nil, err
		}
		intent = merged
		diagnostics = append(diagnostics, mergeDiagnostics...)
	}

	if req.ReasoningConversion == nil {
		normalized, normalizeDiagnostics, err := normalizeIntent(intent)
		if err != nil {
			return Intent{}, nil, err
		}
		return normalized, append(diagnostics, normalizeDiagnostics...), nil
	}
	pivot := Intent{
		Mode:            Mode(req.ReasoningConversion.Mode),
		Effort:          Effort(req.ReasoningConversion.Effort),
		BudgetTokens:    req.ReasoningConversion.BudgetTokens,
		IncludeThoughts: req.ReasoningConversion.IncludeThoughts,
		Source:          SourcePivot,
		BudgetSource:    SourcePivot,
	}
	if req.ReasoningEffort != "" {
		pivotEffort := EffectiveEffort(pivot)
		if Effort(req.ReasoningEffort) == pivotEffort {
			intent.Effort = ""
			intent.Mode = ModeUnset
		}
	}
	merged, mergeDiagnostics, err := MergeExplicit(intent, pivot, req.Model)
	if err != nil {
		return Intent{}, nil, err
	}
	return merged, append(diagnostics, mergeDiagnostics...), nil
}

// ApplyToOpenAIChat writes the portable portion of an intent to the OpenAI
// pivot. reasoning_effort carries level-based strength; a JSON-excluded DTO
// state retains exact budgets and summary visibility across in-process steps.
func ApplyToOpenAIChat(req *dto.GeneralOpenAIRequest, intent Intent) error {
	if req == nil {
		return nil
	}
	intent, _, err := normalizeIntent(intent)
	if err != nil {
		return err
	}

	if effort := EffectiveEffort(intent); effort != "" {
		req.ReasoningEffort = string(effort)
	}

	if intent.IsEmpty() {
		return nil
	}
	req.ReasoningConversion = &dto.ReasoningConversionState{
		Mode:            string(intent.Mode),
		Effort:          string(intent.Effort),
		BudgetTokens:    intent.BudgetTokens,
		IncludeThoughts: intent.IncludeThoughts,
	}
	return nil
}

// ApplyToOpenAIResponses writes the portable portion of an intent directly to
// a Responses request. The JSON-excluded state retains exact provider-native
// controls for any later in-process conversion.
func ApplyToOpenAIResponses(req *dto.OpenAIResponsesRequest, intent Intent) error {
	if req == nil {
		return nil
	}
	intent, _, err := normalizeIntent(intent)
	if err != nil {
		return err
	}

	if effort := EffectiveEffort(intent); effort != "" {
		summary := "detailed"
		if effort == EffortNone || (intent.IncludeThoughts != nil && !*intent.IncludeThoughts) {
			summary = ""
		}
		req.Reasoning = &dto.Reasoning{
			Effort:  string(effort),
			Summary: summary,
		}
	}

	if intent.IsEmpty() {
		return nil
	}
	state := &dto.ReasoningConversionState{
		Mode:            string(intent.Mode),
		Effort:          string(intent.Effort),
		BudgetTokens:    intent.BudgetTokens,
		IncludeThoughts: intent.IncludeThoughts,
	}
	req.ReasoningConversion = state
	return nil
}

func FromOpenAIResponses(req *dto.OpenAIResponsesRequest) (Intent, []types.ConversionDiagnostic, error) {
	if req == nil {
		return Intent{}, nil, nil
	}
	var intent Intent
	if req.Reasoning != nil {
		intent.Source = SourceExplicit
		if req.Reasoning.Effort != "" {
			effort, err := ParseEffort(req.Reasoning.Effort)
			if err != nil {
				return Intent{}, nil, err
			}
			intent.Effort = effort
			intent.Mode = ModeEnabled
			if effort == EffortNone {
				intent.Mode = ModeDisabled
			}
		}
		if req.Reasoning.Summary != "" {
			include := true
			intent.IncludeThoughts = &include
		}
	}
	if req.ReasoningConversion == nil {
		return normalizeIntent(intent)
	}
	pivot := Intent{
		Mode:            Mode(req.ReasoningConversion.Mode),
		Effort:          Effort(req.ReasoningConversion.Effort),
		BudgetTokens:    req.ReasoningConversion.BudgetTokens,
		IncludeThoughts: req.ReasoningConversion.IncludeThoughts,
		Source:          SourcePivot,
		BudgetSource:    SourcePivot,
	}
	if req.Reasoning != nil && req.Reasoning.Effort != "" {
		pivotEffort := EffectiveEffort(pivot)
		if Effort(req.Reasoning.Effort) == pivotEffort {
			intent.Effort = ""
			intent.Mode = ModeUnset
		}
	}
	return MergeExplicit(intent, pivot, req.Model)
}

// FromClaude reads Claude's native thinking and output_config controls. Budget
// bounds are not enforced here: the Claude renderer clamps budgets into
// 1024 <= budget_tokens < max_tokens and reports the adjustment, and other
// targets map the raw budget to an effort.
func FromClaude(req *dto.ClaudeRequest) (Intent, []types.ConversionDiagnostic, error) {
	if req == nil {
		return Intent{}, nil, nil
	}
	var intent Intent
	var diagnostics []types.ConversionDiagnostic
	intent.Source = SourceNative
	if req.Thinking != nil {
		switch req.Thinking.Type {
		case "", "enabled":
			intent.Mode = ModeEnabled
		case "adaptive":
			intent.Mode = ModeAdaptive
		case "disabled":
			intent.Mode = ModeDisabled
			intent.Effort = EffortNone
		default:
			diagnostics = append(diagnostics, types.ConversionDiagnostic{
				Code:     "claude_thinking_type_coerced",
				Path:     "thinking",
				Message:  fmt.Sprintf("unsupported Claude thinking type %q was treated as enabled", req.Thinking.Type),
				Severity: types.ConversionDiagnosticWarning,
			})
			intent.Mode = ModeEnabled
		}
		intent.BudgetTokens = req.Thinking.BudgetTokens
		if req.Thinking.BudgetTokens != nil {
			intent.BudgetSource = SourceNative
		}
		switch req.Thinking.Display {
		case "summarized":
			include := true
			intent.IncludeThoughts = &include
		case "omitted":
			include := false
			intent.IncludeThoughts = &include
		}
	}
	if len(req.OutputConfig) > 0 {
		var output dto.OutputConfigForEffort
		if err := kitutil.Unmarshal(req.OutputConfig, &output); err != nil {
			return Intent{}, nil, fmt.Errorf("invalid Claude output_config: %w", err)
		}
		if output.Effort != "" {
			effort, err := ParseEffort(output.Effort)
			if err != nil {
				return Intent{}, nil, err
			}
			intent.Effort = effort
		}
	}
	if intent.Mode == ModeDisabled && intent.Effort != "" && intent.Effort != EffortNone {
		return intent, diagnostics, nil
	}
	normalized, normalizeDiagnostics, err := normalizeIntent(intent)
	if err != nil {
		return Intent{}, nil, err
	}
	return normalized, append(diagnostics, normalizeDiagnostics...), nil
}

// FromGemini reads a native thinkingConfig. A config carrying both a budget
// and a level is rejected here because the intent alone cannot tell which one
// the target model accepts; NormalizeGeminiThinkingConfig resolves that pair
// first whenever the model's dialect is known.
func FromGemini(req *dto.GeminiChatRequest) (Intent, []types.ConversionDiagnostic, error) {
	if req == nil || req.GenerationConfig.ThinkingConfig == nil {
		return Intent{}, nil, nil
	}
	config := req.GenerationConfig.ThinkingConfig
	if config.ThinkingBudget != nil && config.ThinkingLevel != "" {
		return Intent{}, nil, fmt.Errorf("%w: Gemini thinkingBudget and thinkingLevel cannot both be set", ErrEffortConflict)
	}
	intent := Intent{
		BudgetTokens:    config.ThinkingBudget,
		IncludeThoughts: config.IncludeThoughts,
		Source:          SourceNative,
		BudgetSource:    SourceNative,
	}
	if config.ThinkingLevel != "" {
		effort, err := ParseEffort(config.ThinkingLevel)
		if err != nil {
			return Intent{}, nil, err
		}
		intent.Effort = effort
		intent.Mode = ModeEnabled
		if effort == EffortNone {
			intent.Mode = ModeDisabled
		}
	}
	return normalizeIntent(intent)
}

func EffectiveEffort(intent Intent) Effort {
	intent, _, err := normalizeIntent(intent)
	if err != nil {
		return ""
	}
	if intent.Mode == ModeDisabled {
		return EffortNone
	}
	if intent.Effort != "" {
		return intent.Effort
	}
	if intent.BudgetTokens != nil {
		return EffortFromBudget(*intent.BudgetTokens)
	}
	if intent.Mode == ModeEnabled || intent.Mode == ModeAdaptive {
		return EffortHigh
	}
	return ""
}

func EffortFromBudget(budget int) Effort {
	if budget == 0 {
		return EffortNone
	}
	if budget < 0 {
		return EffortHigh
	}
	if budget <= 1024 {
		return EffortLow
	}
	if budget <= 8192 {
		return EffortMedium
	}
	return EffortHigh
}
