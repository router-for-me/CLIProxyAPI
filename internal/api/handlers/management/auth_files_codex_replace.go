package management

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

var oauthRuntimeMetadataKeys = []string{
	"disabled",
	"status",
	"status_message",
	"unavailable",
	"last_error",
	"quota",
	"model_states",
	"next_retry_after",
	"failed",
	"success",
}

type codexAuthFileRef struct {
	Name      string
	AccountID string
	Disabled  bool
	Metadata  map[string]any
}

// saveCodexOAuthRecord persists a Codex OAuth credential. If this ChatGPT
// account_id already has a file, tokens are written back to that filename so
// usage stats and operator notes stay on the same row. Plan changes do not
// rename the file. A replace hint selects which sibling to keep when several exist.
func (h *Handler) saveCodexOAuthRecord(ctx context.Context, record *coreauth.Auth, replaceHint string) (string, error) {
	if record == nil {
		return "", fmt.Errorf("token record is nil")
	}

	canonicalName := strings.TrimSpace(record.FileName)
	if canonicalName == "" {
		canonicalName = strings.TrimSpace(record.ID)
	}
	accountID := codexAccountIDFromRecord(record)
	replaceHint = filepath.Base(strings.TrimSpace(replaceHint))
	if isUnsafeAuthFileName(replaceHint) {
		replaceHint = ""
	}

	siblings, errList := h.listCodexFilesByAccountID(ctx, accountID)
	if errList != nil {
		return "", errList
	}
	keep, remove := pickCodexKeepAndRemove(siblings, canonicalName, replaceHint)
	writeName := canonicalName
	if keep != "" {
		writeName = keep
		mergeCodexOperatorMetadata(record, metadataForCodexRef(siblings, keep))
	}

	prepareCodexOAuthRecordForSave(record, writeName)

	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		return savedPath, errSave
	}

	for _, name := range remove {
		if name == "" || name == writeName {
			continue
		}
		if _, _, errDelete := h.deleteAuthFileByName(ctx, name); errDelete != nil {
			log.WithError(errDelete).WithField("file", name).Warn("failed to remove replaced Codex auth file")
		}
	}
	return savedPath, nil
}

func prepareCodexOAuthRecordForSave(record *coreauth.Auth, writeName string) {
	if record == nil {
		return
	}
	record.ID = writeName
	record.FileName = writeName
	record.Disabled = false
	if record.Metadata == nil {
		record.Metadata = make(map[string]any)
	}
	record.Metadata["disabled"] = false
	record.Metadata["status"] = ""
	record.Metadata["status_message"] = ""
	record.Metadata["unavailable"] = false
	if setter, ok := record.Storage.(interface{ SetMetadata(map[string]any) }); ok {
		setter.SetMetadata(record.Metadata)
	}
}

func codexAccountIDFromRecord(record *coreauth.Auth) string {
	if record == nil {
		return ""
	}
	if storage, ok := record.Storage.(*codex.CodexTokenStorage); ok {
		if id := strings.TrimSpace(storage.AccountID); id != "" {
			return id
		}
	}
	if record.Metadata == nil {
		return ""
	}
	id, _ := record.Metadata["account_id"].(string)
	return strings.TrimSpace(id)
}

func (h *Handler) listCodexFilesByAccountID(ctx context.Context, accountID string) ([]codexAuthFileRef, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || h == nil {
		return nil, nil
	}
	store := h.tokenStoreWithBaseDir()
	if store == nil {
		return nil, fmt.Errorf("token store unavailable")
	}
	auths, err := store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list existing Codex auth records: %w", err)
	}
	var out []codexAuthFileRef
	for _, auth := range auths {
		if auth == nil || !isCodexAuthRecord(auth) {
			continue
		}
		id := authPayloadAccountID(auth.Metadata)
		if id == "" || id != accountID {
			continue
		}
		name := authRecordFileName(auth)
		if name == "" || isUnsafeAuthFileName(name) {
			continue
		}
		out = append(out, codexAuthFileRef{
			Name:      name,
			AccountID: id,
			Disabled:  auth.Disabled || truthyJSON(auth.Metadata["disabled"]),
			Metadata:  cloneMetadataMap(auth.Metadata),
		})
	}
	return out, nil
}

func isCodexAuthRecord(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return true
	}
	return isCodexAuthPayload(auth.Metadata)
}

func isCodexAuthPayload(payload map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(jsonString(payload["type"])), "codex")
}

func authRecordFileName(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	name := filepath.Base(strings.TrimSpace(auth.FileName))
	if name != "" && name != "." && name != string(filepath.Separator) {
		return name
	}
	return filepath.Base(strings.TrimSpace(auth.ID))
}

func authPayloadAccountID(payload map[string]any) string {
	if id := strings.TrimSpace(jsonString(payload["account_id"])); id != "" {
		return id
	}
	return strings.TrimSpace(jsonString(payload["chatgpt_account_id"]))
}

func jsonString(v any) string {
	s, _ := v.(string)
	return s
}

func truthyJSON(v any) bool {
	switch n := v.(type) {
	case bool:
		return n
	case string:
		switch strings.ToLower(strings.TrimSpace(n)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func pickCodexKeepAndRemove(siblings []codexAuthFileRef, canonicalName, replaceHint string) (keep string, remove []string) {
	canonicalName = strings.TrimSpace(canonicalName)
	replaceHint = strings.TrimSpace(replaceHint)

	if replaceHint != "" {
		for _, sibling := range siblings {
			if sibling.Name == replaceHint {
				keep = replaceHint
				break
			}
		}
	}
	if keep == "" {
		for _, sibling := range siblings {
			if sibling.Name == canonicalName {
				keep = canonicalName
				break
			}
		}
	}
	if keep == "" && len(siblings) == 1 {
		keep = siblings[0].Name
	}
	if keep == "" && len(siblings) > 1 {
		for _, sibling := range siblings {
			if !sibling.Disabled {
				keep = sibling.Name
				break
			}
		}
		if keep == "" {
			keep = siblings[0].Name
		}
	}

	for _, sibling := range siblings {
		if sibling.Name != keep {
			remove = append(remove, sibling.Name)
		}
	}
	return keep, remove
}

func metadataForCodexRef(siblings []codexAuthFileRef, name string) map[string]any {
	for _, sibling := range siblings {
		if sibling.Name == name {
			return sibling.Metadata
		}
	}
	return nil
}

func mergeCodexOperatorMetadata(record *coreauth.Auth, existingMap map[string]any) {
	if record == nil || len(existingMap) == 0 {
		return
	}
	cloned := cloneMetadataMap(existingMap)
	for _, key := range oauthRuntimeMetadataKeys {
		delete(cloned, key)
	}
	coreauth.MergeExistingAuthMetadata(record, cloned)
}

func cloneMetadataMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
