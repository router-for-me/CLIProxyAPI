package geminiwebapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/luispater/CLIProxyAPI/v5/internal/auth/gemini"
	"github.com/luispater/CLIProxyAPI/v5/internal/util"
)

// StoredMessage represents a single message in a conversation record.
type StoredMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

// ConversationRecord stores a full conversation with its metadata for persistence.
type ConversationRecord struct {
	Model     string          `json:"model"`
	ClientID  string          `json:"client_id"`
	Metadata  []string        `json:"metadata,omitempty"`
	Messages  []StoredMessage `json:"messages"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Sha256Hex computes the SHA256 hash of a string and returns its hex representation.
func Sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// RoleText represents a turn in a conversation with a role and text content.
type RoleText struct {
	Role string
	Text string
}

func ToStoredMessages(msgs []RoleText) []StoredMessage {
	out := make([]StoredMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, StoredMessage{
			Role:    m.Role,
			Content: m.Text,
		})
	}
	return out
}

func HashMessage(m StoredMessage) string {
	s := fmt.Sprintf(`{"content":%q,"role":%q}`, m.Content, strings.ToLower(m.Role))
	return Sha256Hex(s)
}

func HashConversation(clientID, model string, msgs []StoredMessage) string {
	var b strings.Builder
	b.WriteString(clientID)
	b.WriteString("|")
	b.WriteString(model)
	for _, m := range msgs {
		b.WriteString("|")
		b.WriteString(HashMessage(m))
	}
	return Sha256Hex(b.String())
}

// ConvStorePath returns the path for account-level metadata persistence based on token file path.
func ConvStorePath(tokenFilePath string) string {
	wd, err := os.Getwd()
	if err != nil || wd == "" {
		wd = "."
	}
	convDir := filepath.Join(wd, "conv")
	base := strings.TrimSuffix(filepath.Base(tokenFilePath), filepath.Ext(tokenFilePath))
	return filepath.Join(convDir, base+".conv.json")
}

// ConvDataPath returns the path for full conversation persistence based on token file path.
func ConvDataPath(tokenFilePath string) string {
	wd, err := os.Getwd()
	if err != nil || wd == "" {
		wd = "."
	}
	convDir := filepath.Join(wd, "conv")
	base := strings.TrimSuffix(filepath.Base(tokenFilePath), filepath.Ext(tokenFilePath))
	return filepath.Join(convDir, base+".data.json")
}

// LoadConvStore reads the account-level metadata store from disk.
func LoadConvStore(path string) (map[string][]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		// Missing file is not an error; return empty map
		return map[string][]string{}, nil
	}
	var tmp map[string][]string
	if err := json.Unmarshal(b, &tmp); err != nil {
		return nil, err
	}
	if tmp == nil {
		tmp = map[string][]string{}
	}
	return tmp, nil
}

// SaveConvStore writes the account-level metadata store to disk atomically.
func SaveConvStore(path string, data map[string][]string) error {
	if data == nil {
		data = map[string][]string{}
	}
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// AccountMetaKey builds the key for account-level metadata map.
func AccountMetaKey(email, modelName string) string {
	return fmt.Sprintf("account-meta|%s|%s", email, modelName)
}

// LoadConvData reads the full conversation data and index from disk.
func LoadConvData(path string) (map[string]ConversationRecord, map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		// Missing file is not an error; return empty sets
		return map[string]ConversationRecord{}, map[string]string{}, nil
	}
	var wrapper struct {
		Items map[string]ConversationRecord `json:"items"`
		Index map[string]string             `json:"index"`
	}
	if err := json.Unmarshal(b, &wrapper); err != nil {
		return nil, nil, err
	}
	if wrapper.Items == nil {
		wrapper.Items = map[string]ConversationRecord{}
	}
	if wrapper.Index == nil {
		wrapper.Index = map[string]string{}
	}
	return wrapper.Items, wrapper.Index, nil
}

// SaveConvData writes the full conversation data and index to disk atomically.
func SaveConvData(path string, items map[string]ConversationRecord, index map[string]string) error {
	if items == nil {
		items = map[string]ConversationRecord{}
	}
	if index == nil {
		index = map[string]string{}
	}
	wrapper := struct {
		Items map[string]ConversationRecord `json:"items"`
		Index map[string]string             `json:"index"`
	}{Items: items, Index: index}
	payload, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// BuildConversationRecord constructs a ConversationRecord from history and the latest output.
// Returns false when output is empty or has no candidates.
func BuildConversationRecord(model, clientID string, history []RoleText, output *ModelOutput, metadata []string) (ConversationRecord, bool) {
	if output == nil || len(output.Candidates) == 0 {
		return ConversationRecord{}, false
	}
	text := ""
	if t := output.Candidates[0].Text; t != "" {
		text = RemoveThinkTags(t)
	}
	final := append([]RoleText{}, history...)
	final = append(final, RoleText{Role: "assistant", Text: text})
	rec := ConversationRecord{
		Model:     model,
		ClientID:  clientID,
		Metadata:  metadata,
		Messages:  ToStoredMessages(final),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	return rec, true
}

// FindByMessageListIn looks up a conversation record by hashed message list.
// It attempts both the stable client ID and a legacy email-based ID.
func FindByMessageListIn(items map[string]ConversationRecord, index map[string]string, stableClientID, email, model string, msgs []RoleText) (ConversationRecord, bool) {
	stored := ToStoredMessages(msgs)
	stableHash := HashConversation(stableClientID, model, stored)
	fallbackHash := HashConversation(email, model, stored)

	// Try stable hash via index indirection first
	if key, ok := index["hash:"+stableHash]; ok {
		if rec, ok2 := items[key]; ok2 {
			return rec, true
		}
	}
	if rec, ok := items[stableHash]; ok {
		return rec, true
	}
	// Fallback to legacy hash (email-based)
	if key, ok := index["hash:"+fallbackHash]; ok {
		if rec, ok2 := items[key]; ok2 {
			return rec, true
		}
	}
	if rec, ok := items[fallbackHash]; ok {
		return rec, true
	}
	return ConversationRecord{}, false
}

// FindConversationIn tries exact then sanitized assistant messages.
func FindConversationIn(items map[string]ConversationRecord, index map[string]string, stableClientID, email, model string, msgs []RoleText) (ConversationRecord, bool) {
	if len(msgs) == 0 {
		return ConversationRecord{}, false
	}
	if rec, ok := FindByMessageListIn(items, index, stableClientID, email, model, msgs); ok {
		return rec, true
	}
	if rec, ok := FindByMessageListIn(items, index, stableClientID, email, model, SanitizeAssistantMessages(msgs)); ok {
		return rec, true
	}
	return ConversationRecord{}, false
}

// FindReusableSessionIn returns reusable metadata and the remaining message suffix.
func FindReusableSessionIn(items map[string]ConversationRecord, index map[string]string, stableClientID, email, model string, msgs []RoleText) ([]string, []RoleText) {
	if len(msgs) < 2 {
		return nil, nil
	}
	searchEnd := len(msgs)
	for searchEnd >= 2 {
		sub := msgs[:searchEnd]
		tail := sub[len(sub)-1]
		if strings.EqualFold(tail.Role, "assistant") || strings.EqualFold(tail.Role, "system") {
			if rec, ok := FindConversationIn(items, index, stableClientID, email, model, sub); ok {
				remain := msgs[searchEnd:]
				return rec.Metadata, remain
			}
		}
		searchEnd--
	}
	return nil, nil
}

// ApplyCookieSnapshotToTokenStorage loads cookies from cookie snapshot into the provided token storage.
// Returns true when a snapshot was found and applied.
func ApplyCookieSnapshotToTokenStorage(tokenFilePath string, ts *gemini.GeminiWebTokenStorage) (bool, error) {
	if ts == nil {
		return false, nil
	}
	var latest gemini.GeminiWebTokenStorage
	if ok, err := util.TryReadCookieSnapshotInto(tokenFilePath, &latest); err != nil {
		return false, err
	} else if !ok {
		return false, nil
	}
	if latest.Secure1PSID != "" {
		ts.Secure1PSID = latest.Secure1PSID
	}
	if latest.Secure1PSIDTS != "" {
		ts.Secure1PSIDTS = latest.Secure1PSIDTS
	}
	return true, nil
}

// SaveCookieSnapshot writes the current cookies into a snapshot file next to the token file.
// This keeps the main token JSON stable until an orderly flush.
func SaveCookieSnapshot(tokenFilePath string, cookies map[string]string) error {
	ts := &gemini.GeminiWebTokenStorage{Type: "gemini-web"}
	if v := cookies["__Secure-1PSID"]; v != "" {
		ts.Secure1PSID = v
	}
	if v := cookies["__Secure-1PSIDTS"]; v != "" {
		ts.Secure1PSIDTS = v
	}
	return util.WriteCookieSnapshot(tokenFilePath, ts)
}

// FlushCookieSnapshotToMain merges the cookie snapshot into the main token file and removes the snapshot.
// If snapshot is missing, it will combine the provided base token storage with the latest cookies.
func FlushCookieSnapshotToMain(tokenFilePath string, cookies map[string]string, base *gemini.GeminiWebTokenStorage) error {
	if tokenFilePath == "" {
		return nil
	}
    var merged gemini.GeminiWebTokenStorage
    var fromSnapshot bool
    if ok, _ := util.TryReadCookieSnapshotInto(tokenFilePath, &merged); ok {
        fromSnapshot = true
    }
    if !fromSnapshot {
        if base != nil {
            merged = *base
        }
        if v := cookies["__Secure-1PSID"]; v != "" {
            merged.Secure1PSID = v
        }
        if v := cookies["__Secure-1PSIDTS"]; v != "" {
            merged.Secure1PSIDTS = v
        }
    }
	merged.Type = "gemini-web"
	if err := merged.SaveTokenToFile(tokenFilePath); err != nil {
		return err
	}
	util.RemoveCookieSnapshots(tokenFilePath)
	return nil
}
