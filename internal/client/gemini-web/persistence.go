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

	bolt "go.etcd.io/bbolt"
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
	return filepath.Join(convDir, base+".bolt")
}

// ConvDataPath returns the path for full conversation persistence based on token file path.
func ConvDataPath(tokenFilePath string) string {
	wd, err := os.Getwd()
	if err != nil || wd == "" {
		wd = "."
	}
	convDir := filepath.Join(wd, "conv")
	base := strings.TrimSuffix(filepath.Base(tokenFilePath), filepath.Ext(tokenFilePath))
	return filepath.Join(convDir, base+".bolt")
}

// LoadConvStore reads the account-level metadata store from disk.
func LoadConvStore(path string) (map[string][]string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := map[string][]string{}
	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("account_meta"))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var arr []string
			if len(v) > 0 {
				if e := json.Unmarshal(v, &arr); e != nil {
					// Skip malformed entries instead of failing the whole load
					return nil
				}
			}
			out[string(k)] = arr
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SaveConvStore writes the account-level metadata store to disk atomically.
func SaveConvStore(path string, data map[string][]string) error {
	if data == nil {
		data = map[string][]string{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return err
	}
	defer db.Close()
	return db.Update(func(tx *bolt.Tx) error {
		// Recreate bucket to reflect the given snapshot exactly.
		if b := tx.Bucket([]byte("account_meta")); b != nil {
			if err := tx.DeleteBucket([]byte("account_meta")); err != nil {
				return err
			}
		}
		b, err := tx.CreateBucket([]byte("account_meta"))
		if err != nil {
			return err
		}
		for k, v := range data {
			enc, e := json.Marshal(v)
			if e != nil {
				return e
			}
			if e := b.Put([]byte(k), enc); e != nil {
				return e
			}
		}
		return nil
	})
}

// AccountMetaKey builds the key for account-level metadata map.
func AccountMetaKey(email, modelName string) string {
	return fmt.Sprintf("account-meta|%s|%s", email, modelName)
}

// LoadConvData reads the full conversation data and index from disk.
func LoadConvData(path string) (map[string]ConversationRecord, map[string]string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	items := map[string]ConversationRecord{}
	index := map[string]string{}
	err = db.View(func(tx *bolt.Tx) error {
		// Load conv_items
		if b := tx.Bucket([]byte("conv_items")); b != nil {
			if e := b.ForEach(func(k, v []byte) error {
				var rec ConversationRecord
				if len(v) > 0 {
					if e2 := json.Unmarshal(v, &rec); e2 != nil {
						// Skip malformed
						return nil
					}
					items[string(k)] = rec
				}
				return nil
			}); e != nil {
				return e
			}
		}
		// Load conv_index
		if b := tx.Bucket([]byte("conv_index")); b != nil {
			if e := b.ForEach(func(k, v []byte) error {
				index[string(k)] = string(v)
				return nil
			}); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return items, index, nil
}

// SaveConvData writes the full conversation data and index to disk atomically.
func SaveConvData(path string, items map[string]ConversationRecord, index map[string]string) error {
	if items == nil {
		items = map[string]ConversationRecord{}
	}
	if index == nil {
		index = map[string]string{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return err
	}
	defer db.Close()
	return db.Update(func(tx *bolt.Tx) error {
		// Recreate items bucket
		if b := tx.Bucket([]byte("conv_items")); b != nil {
			if err := tx.DeleteBucket([]byte("conv_items")); err != nil {
				return err
			}
		}
		bi, err := tx.CreateBucket([]byte("conv_items"))
		if err != nil {
			return err
		}
		for k, rec := range items {
			enc, e := json.Marshal(rec)
			if e != nil {
				return e
			}
			if e := bi.Put([]byte(k), enc); e != nil {
				return e
			}
		}

		// Recreate index bucket
		if b := tx.Bucket([]byte("conv_index")); b != nil {
			if err := tx.DeleteBucket([]byte("conv_index")); err != nil {
				return err
			}
		}
		bx, err := tx.CreateBucket([]byte("conv_index"))
		if err != nil {
			return err
		}
		for k, v := range index {
			if e := bx.Put([]byte(k), []byte(v)); e != nil {
				return e
			}
		}
		return nil
	})
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
