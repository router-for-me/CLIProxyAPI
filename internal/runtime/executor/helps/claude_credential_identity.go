package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/sjson"
)

// ClaudeAgentSessionUUID maps the downstream agent conversation to one stable UUID,
// preserving native Claude Code session signals.
func ClaudeAgentSessionUUID(headers http.Header, originalPayload, translatedPayload []byte, metadataSets ...map[string]any) string {
	return claudeAgentSessionUUID(headers, originalPayload, translatedPayload, metadataSets...)
}

// ClaudeAgentSessionUUIDForRequest preserves Claude-specific session signals only
// for a confirmed native caller. Other callers use protocol session fields,
// execution metadata, or the stable derived conversation root.
func ClaudeAgentSessionUUIDForRequest(headers http.Header, originalPayload, translatedPayload []byte, confirmedClaudeCode bool, metadataSets ...map[string]any) string {
	if !confirmedClaudeCode {
		headers = headers.Clone()
		for key := range headers {
			if strings.EqualFold(key, "X-Claude-Code-Session-Id") {
				delete(headers, key)
			}
		}
		originalPayload = withoutClaudeMetadataUserID(originalPayload)
		translatedPayload = withoutClaudeMetadataUserID(translatedPayload)
	}
	return claudeAgentSessionUUID(headers, originalPayload, translatedPayload, metadataSets...)
}

func claudeAgentSessionUUID(headers http.Header, originalPayload, translatedPayload []byte, metadataSets ...map[string]any) string {
	metadata := mergeClaudeSessionMetadata(metadataSets...)
	identity := cliproxyauth.ExtractSessionID(headers, originalPayload, metadata)
	if identity == "" && len(translatedPayload) > 0 {
		identity = cliproxyauth.ExtractSessionID(headers, translatedPayload, metadata)
	}
	if identity == "" {
		return uuid.NewString()
	}
	if strings.HasPrefix(identity, "claude:") {
		if parsed, errParse := uuid.Parse(strings.TrimPrefix(identity, "claude:")); errParse == nil {
			return parsed.String()
		}
	}
	if parsed, errParse := uuid.Parse(identity); errParse == nil {
		return parsed.String()
	}
	stableInput := "cli-proxy-api\x00claude\x00agent-conversation\x00" + identity
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(stableInput)).String()
}

func withoutClaudeMetadataUserID(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}
	updated, errDelete := sjson.DeleteBytes(payload, "metadata.user_id")
	if errDelete != nil {
		return payload
	}
	return updated
}

func mergeClaudeSessionMetadata(metadataSets ...map[string]any) map[string]any {
	var merged map[string]any
	for _, metadata := range metadataSets {
		if len(metadata) == 0 {
			continue
		}
		if merged == nil {
			merged = make(map[string]any)
		}
		for key, value := range metadata {
			if _, exists := merged[key]; !exists {
				merged[key] = value
			}
		}
	}
	return merged
}

type claudeCredentialDevicePoolKVClient interface {
	KVGet(context.Context, string) ([]byte, bool, error)
	KVSet(context.Context, string, []byte, homekv.KVSetOptions) (bool, error)
}

var currentClaudeCredentialDevicePoolKVClient = func() (claudeCredentialDevicePoolKVClient, bool, error) {
	client, homeMode, errClient := homekv.CurrentKVClient()
	return client, homeMode, errClient
}

// EnsureClaudeCredentialDevicePoolRequired initializes a credential pool locally,
// or coordinates it through Home KV when the selected auth is a remote dispatch clone.
func EnsureClaudeCredentialDevicePoolRequired(ctx context.Context, auth *cliproxyauth.Auth) ([]string, error) {
	if auth == nil {
		return nil, fmt.Errorf("ensure Claude credential device pool: auth is nil")
	}
	rawCredentialDeviceIDs := claudeauth.ReadDeviceIDPool(&auth.Metadata)
	if claudeauth.HasCanonicalDeviceIDPool(rawCredentialDeviceIDs) {
		return claudeauth.NormalizeDeviceIDPool(rawCredentialDeviceIDs), nil
	}
	credentialCandidate := claudeauth.NormalizeDeviceIDPool(rawCredentialDeviceIDs)

	client, homeMode, errClient := currentClaudeCredentialDevicePoolKVClient()
	if !homeMode {
		deviceIDs, _, errEnsure := claudeauth.EnsureDeviceIDPoolFor(&auth.Metadata)
		return deviceIDs, errEnsure
	}
	if errClient != nil {
		return nil, fmt.Errorf("ensure Claude credential device pool: Home KV client: %w", errClient)
	}
	identity := strings.TrimSpace(auth.EnsureIndex())
	if identity == "" {
		identity = strings.TrimSpace(auth.ID)
	}
	if identity == "" {
		return nil, fmt.Errorf("ensure Claude credential device pool: credential identity is empty")
	}
	key := "cpa:claude:credential-device-pool:" + homekv.HashKeyPart(identity)
	if raw, found, errGet := client.KVGet(ctx, key); errGet != nil {
		return nil, fmt.Errorf("ensure Claude credential device pool: Home KV get: %w", errGet)
	} else if found {
		var stored []string
		if errUnmarshal := json.Unmarshal(raw, &stored); errUnmarshal == nil {
			if deviceIDs := claudeauth.NormalizeDeviceIDPool(stored); len(deviceIDs) == claudeauth.ClaudeDevicePoolSize {
				if !claudeauth.HasCanonicalDeviceIDPool(stored) {
					canonicalRaw, errMarshal := json.Marshal(deviceIDs)
					if errMarshal != nil {
						return nil, fmt.Errorf("ensure Claude credential device pool: marshal canonical Home KV value: %w", errMarshal)
					}
					written, errSet := client.KVSet(ctx, key, canonicalRaw, homekv.KVSetOptions{XX: true})
					if errSet != nil {
						return nil, fmt.Errorf("ensure Claude credential device pool: canonicalize Home KV value: %w", errSet)
					}
					if !written {
						return nil, fmt.Errorf("ensure Claude credential device pool: canonical Home KV value was not written")
					}
				}
				claudeauth.StoreDeviceIDPool(&auth.Metadata, deviceIDs)
				return deviceIDs, nil
			}
		}
	}

	deviceIDs := credentialCandidate
	if len(deviceIDs) != claudeauth.ClaudeDevicePoolSize {
		var errGenerate error
		deviceIDs, errGenerate = claudeauth.GenerateDeviceIDPool()
		if errGenerate != nil {
			return nil, errGenerate
		}
	}
	raw, errMarshal := json.Marshal(deviceIDs)
	if errMarshal != nil {
		return nil, fmt.Errorf("ensure Claude credential device pool: marshal Home KV value: %w", errMarshal)
	}
	if _, errSet := client.KVSet(ctx, key, raw, homekv.KVSetOptions{NX: true}); errSet != nil {
		return nil, fmt.Errorf("ensure Claude credential device pool: Home KV set: %w", errSet)
	}
	raw, found, errGet := client.KVGet(ctx, key)
	if errGet != nil {
		return nil, fmt.Errorf("ensure Claude credential device pool: Home KV reread: %w", errGet)
	}
	if !found {
		return nil, fmt.Errorf("ensure Claude credential device pool: Home KV value missing after set")
	}
	var stored []string
	if errUnmarshal := json.Unmarshal(raw, &stored); errUnmarshal != nil {
		return nil, fmt.Errorf("ensure Claude credential device pool: decode Home KV value: %w", errUnmarshal)
	}
	deviceIDs = claudeauth.NormalizeDeviceIDPool(stored)
	if len(deviceIDs) != claudeauth.ClaudeDevicePoolSize {
		return nil, fmt.Errorf("ensure Claude credential device pool: Home KV pool has %d entries, want %d", len(deviceIDs), claudeauth.ClaudeDevicePoolSize)
	}
	claudeauth.StoreDeviceIDPool(&auth.Metadata, deviceIDs)
	return deviceIDs, nil
}

// ClaudeCredentialAccountUUID returns the selected upstream credential's account UUID.
func ClaudeCredentialAccountUUID(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	for _, key := range []string{"account_uuid", "accountUuid"} {
		value := strings.TrimSpace(claudeauth.ReadMetadataString(&auth.Metadata, key))
		if value != "" {
			return value
		}
	}
	return ""
}

type claudeCredentialMetadataRequestError struct {
	cause error
}

func (e *claudeCredentialMetadataRequestError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *claudeCredentialMetadataRequestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *claudeCredentialMetadataRequestError) StatusCode() int {
	if e == nil {
		return 0
	}
	return http.StatusBadRequest
}

func (e *claudeCredentialMetadataRequestError) IsRequestScoped() bool {
	return e != nil
}

func newClaudeCredentialMetadataRequestError(err error) error {
	if err == nil {
		return nil
	}
	return &claudeCredentialMetadataRequestError{cause: err}
}

// ApplyClaudeCredentialMetadata rewrites the identity exception shared by native and cloaked OAuth requests.
func ApplyClaudeCredentialMetadata(payload []byte, auth *cliproxyauth.Auth, sessionID string) ([]byte, string, error) {
	if auth == nil {
		return nil, "", fmt.Errorf("apply Claude credential metadata: auth is nil")
	}
	metadata, metadataPresent, errMetadata := uniqueClaudeJSONObjectMember(payload, "metadata")
	if errMetadata != nil {
		return nil, "", newClaudeCredentialMetadataRequestError(fmt.Errorf("apply Claude credential metadata: %w", errMetadata))
	}
	var existing string
	if metadataPresent {
		trimmedMetadata := bytes.TrimSpace(metadata)
		if len(trimmedMetadata) >= 2 && trimmedMetadata[0] == '{' {
			userID, userIDPresent, errUserID := uniqueClaudeJSONObjectMember(trimmedMetadata, "user_id")
			if errUserID != nil {
				return nil, "", newClaudeCredentialMetadataRequestError(fmt.Errorf("apply Claude credential metadata: metadata: %w", errUserID))
			}
			if userIDPresent && json.Unmarshal(userID, &existing) != nil {
				existing = ""
			}
		}
	}

	deviceIDs, _, errDeviceIDs := claudeauth.EnsureDeviceIDPoolFor(&auth.Metadata)
	if errDeviceIDs != nil {
		return nil, "", errDeviceIDs
	}
	deviceID, errDeviceID := claudeauth.SelectDeviceID(deviceIDs, sessionID)
	if errDeviceID != nil {
		return nil, "", errDeviceID
	}
	accountUUID := ClaudeCredentialAccountUUID(auth)
	if accountUUID == "" {
		return nil, "", fmt.Errorf("apply Claude credential metadata: account UUID is empty")
	}

	encoded, errIdentity := rebuildClaudeMetadataUserID(existing, deviceID, accountUUID, sessionID)
	if errIdentity != nil {
		return nil, "", newClaudeCredentialMetadataRequestError(fmt.Errorf("apply Claude credential metadata: %w", errIdentity))
	}
	updated, errSet := sjson.SetBytes(payload, "metadata.user_id", string(encoded))
	if errSet != nil {
		return nil, "", fmt.Errorf("set Claude credential metadata: %w", errSet)
	}
	return updated, deviceID, nil
}

type claudeJSONMember struct {
	key   string
	value json.RawMessage
}

func uniqueClaudeJSONObjectMember(raw []byte, target string) ([]byte, bool, error) {
	raw = bytes.TrimSpace(raw)
	if !json.Valid(raw) || len(raw) < 2 || raw[0] != '{' {
		return nil, false, fmt.Errorf("request must be a JSON object")
	}

	position := 1
	found := false
	var value []byte
	for {
		position = skipClaudeJSONWhitespace(raw, position)
		if position >= len(raw) {
			return nil, false, fmt.Errorf("unterminated JSON object")
		}
		if raw[position] == '}' {
			break
		}
		keyStart := position
		keyEnd := skipClaudeJSONString(raw, keyStart)
		var key string
		if errUnmarshal := json.Unmarshal(raw[keyStart:keyEnd], &key); errUnmarshal != nil {
			return nil, false, fmt.Errorf("decode JSON object key: %w", errUnmarshal)
		}
		position = skipClaudeJSONWhitespace(raw, keyEnd)
		if position >= len(raw) || raw[position] != ':' {
			return nil, false, fmt.Errorf("JSON object key %q is missing a value", key)
		}
		position = skipClaudeJSONWhitespace(raw, position+1)
		valueStart := position
		position = skipClaudeJSONValue(raw, position)
		if key == target {
			if found {
				return nil, false, fmt.Errorf("duplicate JSON object key %q", target)
			}
			found = true
			value = raw[valueStart:position]
		}
		position = skipClaudeJSONWhitespace(raw, position)
		if position < len(raw) && raw[position] == ',' {
			position++
			continue
		}
		if position >= len(raw) || raw[position] != '}' {
			return nil, false, fmt.Errorf("JSON object key %q has an invalid terminator", key)
		}
	}
	return value, found, nil
}

func skipClaudeJSONWhitespace(raw []byte, position int) int {
	for position < len(raw) {
		switch raw[position] {
		case ' ', '\t', '\r', '\n':
			position++
		default:
			return position
		}
	}
	return position
}

func skipClaudeJSONString(raw []byte, position int) int {
	if position >= len(raw) || raw[position] != '"' {
		return position
	}
	position++
	for position < len(raw) {
		switch raw[position] {
		case '\\':
			position += 2
		case '"':
			return position + 1
		default:
			position++
		}
	}
	return position
}

func skipClaudeJSONValue(raw []byte, position int) int {
	if position >= len(raw) {
		return position
	}
	switch raw[position] {
	case '"':
		return skipClaudeJSONString(raw, position)
	case '{', '[':
		stack := []byte{raw[position]}
		position++
		for position < len(raw) && len(stack) > 0 {
			switch raw[position] {
			case '"':
				position = skipClaudeJSONString(raw, position)
				continue
			case '{', '[':
				stack = append(stack, raw[position])
			case '}', ']':
				stack = stack[:len(stack)-1]
			}
			position++
		}
		return position
	default:
		for position < len(raw) {
			switch raw[position] {
			case ',', '}', ']', ' ', '\t', '\r', '\n':
				return position
			default:
				position++
			}
		}
		return position
	}
}

func rebuildClaudeMetadataUserID(existing, deviceID, accountUUID, sessionID string) ([]byte, error) {
	extras := make([]claudeJSONMember, 0)
	rawExisting := []byte(strings.TrimSpace(existing))
	if json.Valid(rawExisting) && len(rawExisting) >= 2 && rawExisting[0] == '{' {
		decoder := json.NewDecoder(bytes.NewReader(rawExisting))
		_, _ = decoder.Token()
		seen := make(map[string]bool)
		for decoder.More() {
			token, errToken := decoder.Token()
			if errToken != nil {
				return nil, errToken
			}
			key, ok := token.(string)
			if !ok {
				return nil, fmt.Errorf("metadata.user_id contains a non-string key")
			}
			if seen[key] {
				return nil, fmt.Errorf("metadata.user_id contains duplicate key %q", key)
			}
			seen[key] = true
			var value json.RawMessage
			if errDecode := decoder.Decode(&value); errDecode != nil {
				return nil, errDecode
			}
			switch key {
			case "device_id", "account_uuid", "session_id":
			default:
				extras = append(extras, claudeJSONMember{key: key, value: value})
			}
		}
	}

	var output bytes.Buffer
	output.WriteString(`{"device_id":`)
	writeClaudeJSONQuoted(&output, deviceID)
	output.WriteString(`,"account_uuid":`)
	writeClaudeJSONQuoted(&output, accountUUID)
	output.WriteString(`,"session_id":`)
	writeClaudeJSONQuoted(&output, sessionID)
	for _, extra := range extras {
		output.WriteByte(',')
		writeClaudeJSONQuoted(&output, extra.key)
		output.WriteByte(':')
		output.Write(extra.value)
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}

func writeClaudeJSONQuoted(output *bytes.Buffer, value string) {
	encoded, _ := json.Marshal(value)
	output.Write(encoded)
}
