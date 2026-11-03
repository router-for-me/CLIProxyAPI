package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
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

type claudeExecutionMetadataKey struct{}

// WithClaudeExecutionMetadata attaches whether the request had an explicit execution session metadata to ctx.
func WithClaudeExecutionMetadata(ctx context.Context, present bool) context.Context {
	return context.WithValue(ctx, claudeExecutionMetadataKey{}, present)
}

// ClaudeExecutionMetadataFromContext retrieves whether execution metadata was present from ctx.
func ClaudeExecutionMetadataFromContext(ctx context.Context) bool {
	if v, ok := ctx.Value(claudeExecutionMetadataKey{}).(bool); ok {
		return v
	}
	return false
}

// ClaudeRequestHasExecutionMetadata reports whether the request explicitly carried
// an internal execution session metadata key.
func ClaudeRequestHasExecutionMetadata(metadataSets ...map[string]any) bool {
	for _, metadata := range metadataSets {
		if val, ok := metadata[cliproxyexecutor.ExecutionSessionMetadataKey].(string); ok && strings.TrimSpace(val) != "" {
			return true
		}
	}
	return false
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
	updated, errDelete := util.DeleteJSONBytesIfExists(payload, "metadata.user_id")
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

type claudeValidatorPhase uint8

const (
	claudePhaseRoot claudeValidatorPhase = iota
	claudePhaseObjStartOrEnd
	claudePhaseObjKey
	claudePhaseObjColon
	claudePhaseValue
	claudePhaseArrStartOrEnd
	claudePhaseAfterValue
	claudePhaseDone
)

type claudeValidatorToken uint8

const (
	claudeTokNone claudeValidatorToken = iota
	claudeTokString
	claudeTokLiteral
	claudeTokNumMinus
	claudeTokNumZero
	claudeTokNumDigits
	claudeTokNumFracFirst
	claudeTokNumFracDigits
	claudeTokNumExpSign
	claudeTokNumExpFirst
	claudeTokNumExpDigits
)

type claudeRootMetadataValidator struct {
	stack        []byte
	stackBuf     [64]byte
	keyBuf       [64]byte
	keyLen       int
	keyOverflow  bool
	keyHasEsc    bool
	isObjKey     bool
	isTopKey     bool
	seenMetadata bool
	phase        claudeValidatorPhase
	tok          claudeValidatorToken
	strEscape    uint8
	litRem       string
	err          error
}

func isClaudeHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func (v *claudeRootMetadataValidator) fail(err error) (int, error) {
	v.err = err
	return 0, err
}

func (v *claudeRootMetadataValidator) finishTopKey() error {
	if v.keyOverflow {
		return nil
	}
	var isMeta bool
	if !v.keyHasEsc {
		isMeta = string(v.keyBuf[:v.keyLen]) == "metadata"
	} else {
		var rawKey [66]byte
		rawKey[0] = '"'
		copy(rawKey[1:], v.keyBuf[:v.keyLen])
		rawKey[1+v.keyLen] = '"'
		var decoded string
		if errUnmarshal := json.Unmarshal(rawKey[:2+v.keyLen], &decoded); errUnmarshal != nil {
			return fmt.Errorf("decode JSON object key: %w", errUnmarshal)
		}
		isMeta = decoded == "metadata"
	}
	if isMeta {
		if v.seenMetadata {
			return fmt.Errorf("duplicate JSON object key %q", "metadata")
		}
		v.seenMetadata = true
	}
	return nil
}

func (v *claudeRootMetadataValidator) Write(p []byte) (int, error) {
	if v.err != nil {
		return 0, v.err
	}
	i := 0
	for i < len(p) {
		switch v.tok {
		case claudeTokString:
			if v.strEscape == 0 && !v.isTopKey {
				for i < len(p) && p[i] != '"' && p[i] != '\\' && p[i] >= 0x20 {
					i++
				}
				if i >= len(p) {
					return len(p), nil
				}
			}
			c := p[i]
			switch v.strEscape {
			case 0:
				if c < 0x20 {
					return v.fail(fmt.Errorf("request must be a JSON object"))
				}
				if c == '\\' {
					if v.isTopKey {
						v.keyHasEsc = true
						if v.keyLen < len(v.keyBuf) {
							v.keyBuf[v.keyLen] = c
							v.keyLen++
						} else {
							v.keyOverflow = true
						}
					}
					v.strEscape = 1
					i++
					continue
				}
				if c == '"' {
					v.tok = claudeTokNone
					if v.isObjKey {
						v.isObjKey = false
						if v.isTopKey {
							v.isTopKey = false
							if errKey := v.finishTopKey(); errKey != nil {
								return v.fail(errKey)
							}
						}
						v.phase = claudePhaseObjColon
					} else {
						v.phase = claudePhaseAfterValue
					}
					i++
					continue
				}
				if v.isTopKey {
					if v.keyLen < len(v.keyBuf) {
						v.keyBuf[v.keyLen] = c
						v.keyLen++
					} else {
						v.keyOverflow = true
					}
				}
				i++
			case 1:
				if v.isTopKey {
					if v.keyLen < len(v.keyBuf) {
						v.keyBuf[v.keyLen] = c
						v.keyLen++
					} else {
						v.keyOverflow = true
					}
				}
				switch c {
				case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
					v.strEscape = 0
				case 'u':
					v.strEscape = 2
				default:
					return v.fail(fmt.Errorf("request must be a JSON object"))
				}
				i++
			default:
				if !isClaudeHexDigit(c) {
					return v.fail(fmt.Errorf("request must be a JSON object"))
				}
				if v.isTopKey {
					if v.keyLen < len(v.keyBuf) {
						v.keyBuf[v.keyLen] = c
						v.keyLen++
					} else {
						v.keyOverflow = true
					}
				}
				if v.strEscape == 5 {
					v.strEscape = 0
				} else {
					v.strEscape++
				}
				i++
			}
			continue

		case claudeTokLiteral:
			if p[i] != v.litRem[0] {
				return v.fail(fmt.Errorf("request must be a JSON object"))
			}
			v.litRem = v.litRem[1:]
			if len(v.litRem) == 0 {
				v.tok = claudeTokNone
				v.phase = claudePhaseAfterValue
			}
			i++
			continue

		case claudeTokNumMinus:
			c := p[i]
			if c == '0' {
				v.tok = claudeTokNumZero
				i++
				continue
			}
			if c >= '1' && c <= '9' {
				v.tok = claudeTokNumDigits
				i++
				continue
			}
			return v.fail(fmt.Errorf("request must be a JSON object"))

		case claudeTokNumZero:
			c := p[i]
			if c == '.' {
				v.tok = claudeTokNumFracFirst
				i++
				continue
			}
			if c == 'e' || c == 'E' {
				v.tok = claudeTokNumExpSign
				i++
				continue
			}
			v.tok = claudeTokNone
			v.phase = claudePhaseAfterValue

		case claudeTokNumDigits:
			c := p[i]
			if c >= '0' && c <= '9' {
				i++
				continue
			}
			if c == '.' {
				v.tok = claudeTokNumFracFirst
				i++
				continue
			}
			if c == 'e' || c == 'E' {
				v.tok = claudeTokNumExpSign
				i++
				continue
			}
			v.tok = claudeTokNone
			v.phase = claudePhaseAfterValue

		case claudeTokNumFracFirst:
			c := p[i]
			if c >= '0' && c <= '9' {
				v.tok = claudeTokNumFracDigits
				i++
				continue
			}
			return v.fail(fmt.Errorf("request must be a JSON object"))

		case claudeTokNumFracDigits:
			c := p[i]
			if c >= '0' && c <= '9' {
				i++
				continue
			}
			if c == 'e' || c == 'E' {
				v.tok = claudeTokNumExpSign
				i++
				continue
			}
			v.tok = claudeTokNone
			v.phase = claudePhaseAfterValue

		case claudeTokNumExpSign:
			c := p[i]
			if c == '+' || c == '-' {
				v.tok = claudeTokNumExpFirst
				i++
				continue
			}
			if c >= '0' && c <= '9' {
				v.tok = claudeTokNumExpDigits
				i++
				continue
			}
			return v.fail(fmt.Errorf("request must be a JSON object"))

		case claudeTokNumExpFirst:
			c := p[i]
			if c >= '0' && c <= '9' {
				v.tok = claudeTokNumExpDigits
				i++
				continue
			}
			return v.fail(fmt.Errorf("request must be a JSON object"))

		case claudeTokNumExpDigits:
			c := p[i]
			if c >= '0' && c <= '9' {
				i++
				continue
			}
			v.tok = claudeTokNone
			v.phase = claudePhaseAfterValue
		}

		c := p[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			i++
			continue
		}

		switch v.phase {
		case claudePhaseRoot:
			if c != '{' {
				return v.fail(fmt.Errorf("request must be a JSON object"))
			}
			v.stack = append(v.stack, '{')
			v.phase = claudePhaseObjStartOrEnd
			i++

		case claudePhaseObjStartOrEnd:
			if c == '}' {
				v.stack = v.stack[:len(v.stack)-1]
				if len(v.stack) == 0 {
					v.phase = claudePhaseDone
				} else {
					v.phase = claudePhaseAfterValue
				}
				i++
				continue
			}
			if c != '"' {
				return v.fail(fmt.Errorf("request must be a JSON object"))
			}
			v.tok = claudeTokString
			v.strEscape = 0
			v.isObjKey = true
			v.isTopKey = len(v.stack) == 1
			v.keyLen = 0
			v.keyOverflow = false
			v.keyHasEsc = false
			i++

		case claudePhaseObjKey:
			if c != '"' {
				return v.fail(fmt.Errorf("request must be a JSON object"))
			}
			v.tok = claudeTokString
			v.strEscape = 0
			v.isObjKey = true
			v.isTopKey = len(v.stack) == 1
			v.keyLen = 0
			v.keyOverflow = false
			v.keyHasEsc = false
			i++

		case claudePhaseObjColon:
			if c != ':' {
				return v.fail(fmt.Errorf("request must be a JSON object"))
			}
			v.phase = claudePhaseValue
			i++

		case claudePhaseArrStartOrEnd:
			if c == ']' {
				v.stack = v.stack[:len(v.stack)-1]
				if len(v.stack) == 0 {
					v.phase = claudePhaseDone
				} else {
					v.phase = claudePhaseAfterValue
				}
				i++
				continue
			}
			v.phase = claudePhaseValue
			continue

		case claudePhaseValue:
			switch c {
			case '{':
				v.stack = append(v.stack, '{')
				v.phase = claudePhaseObjStartOrEnd
				i++
			case '[':
				v.stack = append(v.stack, '[')
				v.phase = claudePhaseArrStartOrEnd
				i++
			case '"':
				v.tok = claudeTokString
				v.strEscape = 0
				v.isObjKey = false
				v.isTopKey = false
				i++
			case 't':
				v.tok = claudeTokLiteral
				v.litRem = "rue"
				i++
			case 'f':
				v.tok = claudeTokLiteral
				v.litRem = "alse"
				i++
			case 'n':
				v.tok = claudeTokLiteral
				v.litRem = "ull"
				i++
			case '-':
				v.tok = claudeTokNumMinus
				i++
			case '0':
				v.tok = claudeTokNumZero
				i++
			default:
				if c >= '1' && c <= '9' {
					v.tok = claudeTokNumDigits
					i++
				} else {
					return v.fail(fmt.Errorf("request must be a JSON object"))
				}
			}

		case claudePhaseAfterValue:
			if len(v.stack) == 0 {
				return v.fail(fmt.Errorf("request must be a JSON object"))
			}
			top := v.stack[len(v.stack)-1]
			if top == '{' {
				if c == ',' {
					v.phase = claudePhaseObjKey
					i++
					continue
				}
				if c == '}' {
					v.stack = v.stack[:len(v.stack)-1]
					if len(v.stack) == 0 {
						v.phase = claudePhaseDone
					}
					i++
					continue
				}
				return v.fail(fmt.Errorf("request must be a JSON object"))
			}
			if c == ',' {
				v.phase = claudePhaseValue
				i++
				continue
			}
			if c == ']' {
				v.stack = v.stack[:len(v.stack)-1]
				if len(v.stack) == 0 {
					v.phase = claudePhaseDone
				}
				i++
				continue
			}
			return v.fail(fmt.Errorf("request must be a JSON object"))

		case claudePhaseDone:
			return v.fail(fmt.Errorf("request must be a JSON object"))
		}
	}
	return len(p), nil
}

func validateClaudeDocMetadataRoot(doc *ojson.Doc) error {
	if doc == nil || doc.Len() == 0 {
		return fmt.Errorf("request must be a JSON object")
	}
	v := claudeRootMetadataValidator{}
	v.stack = v.stackBuf[:0]
	if _, err := doc.WriteTo(&v); err != nil {
		return err
	}
	if v.err != nil {
		return v.err
	}
	if v.tok != claudeTokNone || v.phase != claudePhaseDone || len(v.stack) != 0 {
		return fmt.Errorf("request must be a JSON object")
	}
	return nil
}

// ApplyClaudeCredentialMetadata rewrites the identity exception shared by native and cloaked OAuth requests.
func ApplyClaudeCredentialMetadata(payload []byte, auth *cliproxyauth.Auth, sessionID string) ([]byte, string, error) {
	doc := ojson.NewDoc(payload)
	deviceID, errApply := ApplyClaudeCredentialMetadataDoc(doc, auth, sessionID)
	if errApply != nil {
		return nil, "", errApply
	}
	return doc.Bytes(), deviceID, nil
}

// ApplyClaudeCredentialMetadataDoc rewrites the identity exception in place on doc.
func ApplyClaudeCredentialMetadataDoc(doc *ojson.Doc, auth *cliproxyauth.Auth, sessionID string) (string, error) {
	if doc == nil {
		return "", fmt.Errorf("apply Claude credential metadata: doc is nil")
	}
	if auth == nil {
		return "", fmt.Errorf("apply Claude credential metadata: auth is nil")
	}
	metadataNode := doc.Get("metadata")
	if errMetadata := validateClaudeDocMetadataRoot(doc); errMetadata != nil {
		return "", newClaudeCredentialMetadataRequestError(fmt.Errorf("apply Claude credential metadata: %w", errMetadata))
	}
	var existing string
	if metadataNode.Exists() && metadataNode.IsObject() {
		trimmedMetadata := bytes.TrimSpace([]byte(metadataNode.Raw))
		if len(trimmedMetadata) >= 2 && trimmedMetadata[0] == '{' {
			userID, userIDPresent, errUserID := uniqueClaudeJSONObjectMember(trimmedMetadata, "user_id")
			if errUserID != nil {
				return "", newClaudeCredentialMetadataRequestError(fmt.Errorf("apply Claude credential metadata: metadata: %w", errUserID))
			}
			if userIDPresent && json.Unmarshal(userID, &existing) != nil {
				existing = ""
			}
		}
	}

	deviceIDs, _, errDeviceIDs := claudeauth.EnsureDeviceIDPoolFor(&auth.Metadata)
	if errDeviceIDs != nil {
		return "", errDeviceIDs
	}
	deviceID, errDeviceID := claudeauth.SelectDeviceID(deviceIDs, sessionID)
	if errDeviceID != nil {
		return "", errDeviceID
	}
	accountUUID := ClaudeCredentialAccountUUID(auth)
	if accountUUID == "" {
		return "", fmt.Errorf("apply Claude credential metadata: account UUID is empty")
	}

	encoded, errIdentity := rebuildClaudeMetadataUserID(existing, deviceID, accountUUID, sessionID)
	if errIdentity != nil {
		return "", newClaudeCredentialMetadataRequestError(fmt.Errorf("apply Claude credential metadata: %w", errIdentity))
	}
	if errSet := doc.Set("metadata.user_id", string(encoded)); errSet != nil {
		return "", newClaudeCredentialMetadataRequestError(fmt.Errorf("set Claude credential metadata: %w", errSet))
	}
	return deviceID, nil
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
			if position+1 >= len(raw) {
				return len(raw)
			}
			position += 2
		case '"':
			return position + 1
		default:
			position++
		}
	}
	return len(raw)
}

func skipClaudeJSONValue(raw []byte, position int) int {
	if position >= len(raw) {
		return len(raw)
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
		if position > len(raw) {
			return len(raw)
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
		return len(raw)
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
