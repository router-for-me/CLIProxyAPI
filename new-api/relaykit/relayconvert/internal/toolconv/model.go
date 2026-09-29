package toolconv

import (
	"encoding/json"

	"github.com/QuantumNous/new-api/relaykit/types"
)

type Kind string

const (
	KindFunction      Kind = "function"
	KindWebSearch     Kind = "web_search"
	KindFileSearch    Kind = "file_search"
	KindWebFetch      Kind = "web_fetch"
	KindCodeExecution Kind = "code_execution"
	KindComputerUse   Kind = "computer_use"
	KindURLContext    Kind = "url_context"
	KindMCP           Kind = "mcp"
	KindImage         Kind = "image_generation"
	KindNative        Kind = "native"
)

type Execution string

const (
	ExecutionClient Execution = "client"
	ExecutionServer Execution = "server"
)

type Function struct {
	Name        string
	Description string
	Parameters  any
	Strict      *bool
}

type ApproximateLocation struct {
	City     string
	Region   string
	Country  string
	Timezone string
}

type WebSearch struct {
	Location          *ApproximateLocation
	AllowedDomains    []string
	BlockedDomains    []string
	SearchContextSize string
	MaxUses           *int
	AllowedCallers    []string
	ResponseInclusion string
	ExternalWebAccess *bool
	ReturnTokenBudget json.RawMessage
}

type Definition struct {
	Kind       Kind
	Execution  Execution
	NativeType string
	Name       string
	Function   *Function
	WebSearch  *WebSearch
	Raw        json.RawMessage
	Group      int
}

type ChoiceMode string

const (
	ChoiceAuto     ChoiceMode = "auto"
	ChoiceNone     ChoiceMode = "none"
	ChoiceRequired ChoiceMode = "required"
	ChoiceNamed    ChoiceMode = "named"
	ChoiceOpaque   ChoiceMode = "opaque"
)

type Choice struct {
	Mode                   ChoiceMode
	Kind                   Kind
	Name                   string
	AllowedNames           []string
	NativeType             string
	DisableParallelToolUse *bool
	Raw                    json.RawMessage
}

type Set struct {
	Source           types.RelayFormat
	Definitions      []Definition
	Choice           *Choice
	ParallelAllowed  *bool
	NativeToolConfig json.RawMessage
	History          []HostedHistoryItem
}

func (s Set) Empty() bool {
	return len(s.Definitions) == 0 && s.Choice == nil && s.ParallelAllowed == nil && len(s.NativeToolConfig) == 0 && len(s.History) == 0
}

type HostedHistoryItem struct {
	Kind              Kind
	NativeType        string
	Role              string
	MessageIndex      int
	BlockIndex        int
	MessageHasRegular bool
	Sequence          int
	ID                string
	CallID            string
	Name              string
	ServerName        string
	Status            string
	Action            json.RawMessage
	Results           json.RawMessage
	Caller            json.RawMessage
	Raw               json.RawMessage
}
