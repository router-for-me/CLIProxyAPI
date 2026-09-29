package controller

import "github.com/QuantumNous/new-api/model"

func channelHasSensitiveChanges(channel *PatchChannel, origin *model.Channel, requestData map[string]any) bool {
	if _, ok := requestData["type"]; ok && channel.Type != origin.Type {
		return true
	}
	if _, ok := requestData["key"]; ok && channel.Key != "" && channel.Key != origin.Key {
		return true
	}
	if _, ok := requestData["base_url"]; ok && !equalStringPtr(channel.BaseURL, origin.BaseURL) {
		return true
	}
	if _, ok := requestData["openai_organization"]; ok && !equalStringPtr(channel.OpenAIOrganization, origin.OpenAIOrganization) {
		return true
	}
	if _, ok := requestData["header_override"]; ok && !equalStringPtr(channel.HeaderOverride, origin.HeaderOverride) {
		return true
	}
	if _, ok := requestData["param_override"]; ok && !equalStringPtr(channel.ParamOverride, origin.ParamOverride) {
		return true
	}
	if _, ok := requestData["setting"]; ok && !equalStringPtr(channel.Setting, origin.Setting) {
		return true
	}
	if _, ok := requestData["other"]; ok && channel.Other != origin.Other {
		return true
	}
	if _, ok := requestData["settings"]; ok && channel.OtherSettings != origin.OtherSettings {
		return true
	}
	if _, ok := requestData["key_mode"]; ok && channel.KeyMode != nil {
		return true
	}
	// Fail closed: any field present in the request that is neither a known
	// sensitive field (gated above) nor an explicitly classified non-sensitive
	// field must be treated as sensitive. This keeps a newly added channel field
	// from silently becoming editable by ChannelWrite-only admins until it is
	// consciously classified in channelNonSensitiveFields.
	for field := range requestData {
		if _, ok := channelSensitiveFields[field]; ok {
			continue
		}
		if _, ok := channelNonSensitiveFields[field]; ok {
			continue
		}
		if _, ok := channelOperationalFields[field]; ok {
			continue
		}
		if _, ok := channelReadOnlyFields[field]; ok {
			continue
		}
		return true
	}
	return false
}

// channelSensitiveFields lists the channel fields whose modification requires
// ChannelSensitiveWrite. They are each checked individually in
// channelHasSensitiveChanges with a precise old-vs-new comparison; this set is
// used to exclude them from the fail-closed scan for unknown fields.
var channelSensitiveFields = map[string]struct{}{
	"type":                {},
	"key":                 {},
	"base_url":            {},
	"openai_organization": {},
	"header_override":     {},
	"param_override":      {},
	"setting":             {},
	"other":               {},
	"settings":            {},
	"key_mode":            {},
}

// channelOperationalFields lists fields managed by operation endpoints instead
// of the general channel edit endpoint.
var channelOperationalFields = map[string]struct{}{
	"status": {},
}

// channelReadOnlyFields lists server-managed/accounting fields that the general
// channel edit endpoint must ignore even if a client sends them.
var channelReadOnlyFields = map[string]struct{}{
	"created_time":         {},
	"test_time":            {},
	"response_time":        {},
	"balance":              {},
	"balance_updated_time": {},
	"used_quota":           {},
}

func clearChannelReadOnlyFields(channel *PatchChannel, requestData map[string]any) {
	if _, ok := requestData["created_time"]; ok {
		channel.CreatedTime = 0
	}
	if _, ok := requestData["test_time"]; ok {
		channel.TestTime = 0
	}
	if _, ok := requestData["response_time"]; ok {
		channel.ResponseTime = 0
	}
	if _, ok := requestData["balance"]; ok {
		channel.Balance = 0
	}
	if _, ok := requestData["balance_updated_time"]; ok {
		channel.BalanceUpdatedTime = 0
	}
	if _, ok := requestData["used_quota"]; ok {
		channel.UsedQuota = 0
	}
}

// channelNonSensitiveFields lists routing / server-managed channel
// fields a ChannelWrite admin may edit without ChannelSensitiveWrite. When a new
// field is added to model.Channel it must be added to either this set or
// channelSensitiveFields or channelOperationalFields; otherwise it falls through
// to the fail-closed branch and is treated as sensitive. The
// TestChannelFieldsAreClassified guard test enforces this.
var channelNonSensitiveFields = map[string]struct{}{
	"id":                  {},
	"test_model":          {},
	"name":                {},
	"weight":              {},
	"models":              {},
	"group":               {},
	"model_mapping":       {},
	"status_code_mapping": {},
	"priority":            {},
	"auto_ban":            {},
	"other_info":          {},
	"tag":                 {},
	"remark":              {},
	"channel_info":        {},
	"multi_key_mode":      {},
}
