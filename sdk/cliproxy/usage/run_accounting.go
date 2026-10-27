package usage

import "strings"

// UsageAccounting is a reference to provider-owned Run accounting, which can
// span several requests. It deliberately contains no token counters.
type UsageAccounting struct {
	Provider string `json:"provider"`
	Scope    string `json:"scope"`
	RecordID string `json:"record_id"`
	State    string `json:"state"`
	Replay   bool   `json:"replay"`
	Resumed  bool   `json:"resumed"`
}

// CloneUsageAccounting returns a detached, validated reference or nil. Unknown
// providers and scopes are ignored until their accounting contract is supported.
func CloneUsageAccounting(ref *UsageAccounting) *UsageAccounting {
	if ref == nil || ref.Provider != "cursor-next" || ref.Scope != "run" || len(ref.RecordID) != 64 {
		return nil
	}
	for _, c := range ref.RecordID {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return nil
		}
	}
	switch ref.State {
	case "pending", "running", "measured", "deferred", "unknown":
	default:
		return nil
	}
	copy := *ref
	copy.Provider = strings.Clone(copy.Provider)
	copy.Scope = strings.Clone(copy.Scope)
	copy.RecordID = strings.Clone(copy.RecordID)
	copy.State = strings.Clone(copy.State)
	return &copy
}
