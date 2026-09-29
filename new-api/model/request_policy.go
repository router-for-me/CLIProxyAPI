package model

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
)

// RequestPolicySnapshot is the validated read model behind the request policy
// settings API. It is rebuilt from the option map on every option write, so
// the runtime globals updated by the same write never disagree with it.
type RequestPolicySnapshot struct {
	Affinity        operation_setting.ChannelAffinitySetting
	RetryTimes      int
	RetryCodes      []operation_setting.StatusCodeRange
	DisableCodes    []operation_setting.StatusCodeRange
	DisableKeywords []string
	CheckText       bool
	TextKeywords    []string
	AutoDisable     bool
	Options         map[string]string
}

var requestPolicySnapshot atomic.Pointer[RequestPolicySnapshot]
var requestPolicyOptionMutex sync.Mutex
var requestPolicyDefaults = requestPolicyDefaultOptions()

func requestPolicyDefaultOptions() map[string]string {
	defaults := make(map[string]string)
	for prefix, value := range map[string]any{
		"channel_affinity_setting.": operation_setting.GetChannelAffinitySetting(),
		"monitor_setting.":          operation_setting.GetMonitorSetting(),
	} {
		fields, err := config.ConfigToMap(value)
		if err != nil {
			panic(err)
		}
		for field, value := range fields {
			defaults[prefix+field] = value
		}
	}
	defaults["RetryTimes"] = strconv.Itoa(common.RetryTimes)
	defaults["AutomaticRetryStatusCodes"] = operation_setting.AutomaticRetryStatusCodesToString()
	defaults["AutomaticDisableStatusCodes"] = operation_setting.AutomaticDisableStatusCodesToString()
	defaults["AutomaticDisableKeywords"] = operation_setting.AutomaticDisableKeywordsToString()
	defaults["AutomaticDisableChannelEnabled"] = strconv.FormatBool(common.AutomaticDisableChannelEnabled)
	defaults["CheckSensitiveEnabled"] = strconv.FormatBool(setting.CheckSensitiveEnabled)
	defaults["CheckSensitiveOnPromptEnabled"] = strconv.FormatBool(setting.CheckSensitiveOnPromptEnabled)
	defaults["SensitiveWords"] = setting.SensitiveWordsToString()
	defaults["AutomaticEnableChannelEnabled"] = strconv.FormatBool(common.AutomaticEnableChannelEnabled)
	defaults["ChannelDisableThreshold"] = strconv.FormatFloat(common.ChannelDisableThreshold, 'f', -1, 64)
	return defaults
}

func IsRequestPolicyOption(key string) bool {
	if strings.HasPrefix(key, "channel_affinity_setting.") {
		return true
	}
	switch key {
	case "CheckSensitiveEnabled", "CheckSensitiveOnPromptEnabled", "SensitiveWords", "AutomaticEnableChannelEnabled", "ChannelDisableThreshold", "monitor_setting.auto_test_channel_enabled", "monitor_setting.auto_test_channel_minutes", "monitor_setting.channel_test_concurrency", "monitor_setting.channel_test_mode", "RetryTimes", "AutomaticRetryStatusCodes", "AutomaticDisableChannelEnabled", "AutomaticDisableStatusCodes", "AutomaticDisableKeywords":
		return true
	}
	return false
}

func CurrentRequestPolicy() *RequestPolicySnapshot {
	if snapshot := requestPolicySnapshot.Load(); snapshot != nil {
		return snapshot
	}
	// Startup and test fixtures may not have loaded options yet.
	snapshot, err := BuildRequestPolicy(nil)
	if err != nil {
		return &RequestPolicySnapshot{Options: map[string]string{}}
	}
	return snapshot
}

func BuildRequestPolicy(options map[string]string) (*RequestPolicySnapshot, error) {
	defaults := requestPolicyDefaults

	raw := make(map[string]string)
	for key, value := range defaults {
		if IsRequestPolicyOption(key) {
			raw[key] = value
		}
	}
	maps.Copy(raw, options)
	snapshot := &RequestPolicySnapshot{Options: raw}
	affinityFields := map[string]string{}
	for key, value := range raw {
		if field, ok := strings.CutPrefix(key, "channel_affinity_setting."); ok {
			switch field {
			case "enabled", "session_mode", "switch_on_success", "keep_on_channel_disabled", "max_entries", "default_ttl_seconds", "rules":
			default:
				return nil, fmt.Errorf("unknown affinity option: %s", key)
			}
			affinityFields[field] = value
		}
	}
	// Decode through JSON so malformed scalar/array values cannot be silently ignored.
	affinityJSON := map[string]json.RawMessage{}
	for key, value := range affinityFields {
		if key == "session_mode" {
			encoded, _ := common.Marshal(value)
			affinityJSON[key] = encoded
		} else {
			affinityJSON[key] = json.RawMessage(value)
		}
	}
	encoded, err := common.Marshal(affinityJSON)
	if err != nil {
		return nil, err
	}
	if err := common.Unmarshal(encoded, &snapshot.Affinity); err != nil {
		return nil, err
	}
	snapshot.RetryTimes, err = strconv.Atoi(raw["RetryTimes"])
	if err != nil || snapshot.RetryTimes < 0 || snapshot.RetryTimes == math.MaxInt {
		return nil, fmt.Errorf("retry times must be a non-negative integer with room for the initial attempt")
	}
	snapshot.AutoDisable, err = strconv.ParseBool(raw["AutomaticDisableChannelEnabled"])
	if err != nil {
		return nil, err
	}
	snapshot.RetryCodes, err = operation_setting.ParseHTTPStatusCodeRanges(raw["AutomaticRetryStatusCodes"])
	if err != nil {
		return nil, err
	}
	snapshot.DisableCodes, err = operation_setting.ParseHTTPStatusCodeRanges(raw["AutomaticDisableStatusCodes"])
	if err != nil {
		return nil, err
	}
	snapshot.DisableKeywords = strings.Split(raw["AutomaticDisableKeywords"], "\n")
	for _, key := range []string{"CheckSensitiveEnabled", "CheckSensitiveOnPromptEnabled", "AutomaticEnableChannelEnabled", "monitor_setting.auto_test_channel_enabled"} {
		if _, err := strconv.ParseBool(raw[key]); err != nil {
			return nil, fmt.Errorf("invalid boolean: %s", key)
		}
	}
	snapshot.CheckText = raw["CheckSensitiveEnabled"] == "true" && raw["CheckSensitiveOnPromptEnabled"] == "true"
	for word := range strings.SplitSeq(raw["SensitiveWords"], "\n") {
		if word = strings.TrimSpace(word); word != "" {
			snapshot.TextKeywords = append(snapshot.TextKeywords, word)
		}
	}
	if err := operation_setting.ValidateChannelTestConcurrency(raw["monitor_setting.channel_test_concurrency"]); err != nil {
		return nil, err
	}
	for _, key := range []string{"ChannelDisableThreshold", "monitor_setting.auto_test_channel_minutes"} {
		if raw[key] == "" && key == "ChannelDisableThreshold" {
			continue
		}
		value, err := strconv.ParseFloat(raw[key], 64)
		if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("invalid numeric value: %s", key)
		}
	}
	switch raw["monitor_setting.channel_test_mode"] {
	case "scheduled_all", "auto_ban_only", "passive_recovery":
	default:
		return nil, fmt.Errorf("invalid channel test mode")
	}

	if snapshot.Affinity.MaxEntries < 0 || snapshot.Affinity.DefaultTTLSeconds < 0 {
		return nil, fmt.Errorf("invalid affinity cache limits")
	}
	switch snapshot.Affinity.SessionMode {
	case "", "off", "prefer", "strict":
	default:
		return nil, fmt.Errorf("invalid global session mode")
	}
	for _, rule := range snapshot.Affinity.Rules {
		switch rule.SessionMode {
		case "", "inherit", "off", "prefer", "strict":
		default:
			return nil, fmt.Errorf("invalid session mode for rule %q", rule.Name)
		}
		patterns := append(append([]string{}, rule.ModelRegex...), rule.PathRegex...)
		if rule.ValueRegex != "" {
			patterns = append(patterns, rule.ValueRegex)
		}
		for _, pattern := range patterns {
			if _, err := regexp.Compile(pattern); err != nil {
				return nil, fmt.Errorf("rule %q: %w", rule.Name, err)
			}
		}
		if rule.TTLSeconds < 0 {
			return nil, fmt.Errorf("negative affinity TTL")
		}
	}
	return snapshot, nil
}

func refreshRequestPolicySnapshot() error {
	common.OptionMapRWMutex.RLock()
	options := make(map[string]string)
	for key, value := range common.OptionMap {
		if IsRequestPolicyOption(key) {
			options[key] = value
		}
	}
	common.OptionMapRWMutex.RUnlock()
	snapshot, err := BuildRequestPolicy(options)
	if err == nil {
		requestPolicySnapshot.Store(snapshot)
	}
	return err
}

// UpdateRequestPolicyOptions validates the entire resulting policy before any
// write. Unknown JSON rule fields stay in the original option string.
func UpdateRequestPolicyOptions(values map[string]string) error {
	requestPolicyOptionMutex.Lock()
	defer requestPolicyOptionMutex.Unlock()
	options := maps.Clone(CurrentRequestPolicy().Options)
	for key, value := range values {
		if !IsRequestPolicyOption(key) {
			return fmt.Errorf("not a request policy option: %s", key)
		}
		options[key] = value
	}
	snapshot, err := BuildRequestPolicy(options)
	if err != nil {
		return err
	}
	if err := DB.Transaction(func(tx *gorm.DB) error {
		for key, value := range values {
			option := Option{Key: key}
			if err := tx.FirstOrCreate(&option, Option{Key: key}).Error; err != nil {
				return err
			}
			if err := tx.Model(&option).Update("value", value).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for key, value := range values {
		if err := updateOptionMap(key, value); err != nil {
			return err
		}
	}
	requestPolicySnapshot.Store(snapshot)
	return nil
}
