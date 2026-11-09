package management

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/multiuser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) SetHubStore(store *multiuser.Store) { h.hubStore = store }

func hubError(c *gin.Context, err error) {
	status := 500
	switch {
	case errors.Is(err, context.Canceled):
		c.Status(499)
		return
	case errors.Is(err, multiuser.ErrDenied):
		status = 403
	case errors.Is(err, multiuser.ErrInvalid):
		status = 400
	case errors.Is(err, multiuser.ErrNotFound):
		status = 404
	case errors.Is(err, multiuser.ErrConflict):
		status = 409
	case errors.Is(err, multiuser.ErrJournalBuilding):
		c.Header("Retry-After", "2")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error(), "code": "journal_index_building"})
		return
	}
	if status == 500 {
		log.WithError(err).Error("multi-user: credential operation failed")
		c.JSON(status, gin.H{"error": "credential operation could not be saved"})
		return
	}
	c.JSON(status, gin.H{"error": err.Error()})
}

func (h *Handler) hubChannel(c *gin.Context, id string) (multiuser.Channel, bool) {
	channel, ok := h.hubStore.Channel(id)
	if !ok {
		hubError(c, multiuser.ErrNotFound)
		return channel, false
	}
	user := multiuser.CurrentUser(c)
	if channel.Owner != user.Username && user.Role != "admin" && !channel.Public {
		hubError(c, multiuser.ErrDenied)
		return channel, false
	}
	return channel, true
}

func (h *Handler) hubCredential(c *gin.Context, manage bool) (*coreauth.Auth, bool) {
	id, err := base64.RawURLEncoding.DecodeString(c.Param("id"))
	if err != nil {
		hubError(c, multiuser.ErrInvalid)
		return nil, false
	}
	if h.authManager == nil {
		c.JSON(503, gin.H{"error": "credential manager unavailable"})
		return nil, false
	}
	a, ok := h.authManager.GetByID(string(id))
	if !ok {
		hubError(c, multiuser.ErrNotFound)
		return nil, false
	}
	user := multiuser.CurrentUser(c)
	if manage && a.Owner() != user.Username && user.Role != "admin" && !h.hubStore.CanManageChannel(user, a.ChannelID()) {
		hubError(c, multiuser.ErrDenied)
		return nil, false
	}
	c.Set("hubTarget", a.ID)
	return a, true
}

// hubMetadataInt reads an integer stored in credential metadata (JSON numbers
// unmarshal as float64).
func hubMetadataInt(metadata map[string]any, key string) (int, bool) {
	if metadata == nil {
		return 0, false
	}
	switch value := metadata[key].(type) {
	case float64:
		return int(value), true
	case int:
		return value, true
	}
	return 0, false
}

// hubCredentialKind distinguishes OAuth credential files from channel-mounted
// AI providers (OpenAI-compatible upstreams stored as auth files).
func hubCredentialKind(a *coreauth.Auth) string {
	if a == nil {
		return "credential"
	}
	if strings.EqualFold(strings.TrimSpace(a.Provider), "openai-compatible") || strings.HasPrefix(strings.TrimSpace(a.Provider), "openai-compatible-") {
		return "provider"
	}
	if a.Attributes != nil && strings.TrimSpace(a.Attributes["compat_name"]) != "" {
		return "provider"
	}
	return "credential"
}

// stampHubCredentialChannel writes the channel ownership metadata and the pinned
// routing prefix ("slug-type#n") onto a credential being attached to a channel.
// The per-(channel, type) index is allocated once and reused on re-saves; a
// re-login or re-upload of the same account file keeps its original index.
func (h *Handler) stampHubCredentialChannel(actor multiuser.User, ch multiuser.Channel, record *coreauth.Auth) error {
	if record.Metadata == nil {
		record.Metadata = make(map[string]any)
	}
	credType := strings.ToLower(strings.TrimSpace(record.Provider))
	if credType == "" {
		return fmt.Errorf("%w: credential type is required", multiuser.ErrInvalid)
	}
	index, hasIndex := hubMetadataInt(record.Metadata, "channel_index")
	existingChannel, _ := record.Metadata["channel_id"].(string)
	if !hasIndex || index <= 0 || existingChannel != ch.ID {
		index = h.findChannelCredentialIndex(ch, credType, record)
		if index == 0 {
			allocated, err := h.hubStore.AllocateCredentialIndex(ch.ID, credType)
			if err != nil {
				return err
			}
			index = allocated
		}
	}
	pinned := fmt.Sprintf("%s-%s#%d", ch.Prefix, credType, index)
	record.Metadata["owner"] = ch.Owner
	record.Metadata["channel_id"] = ch.ID
	delete(record.Metadata, "channel_name")
	record.Metadata["channel_prefix"] = ch.Prefix
	record.Metadata["credential_type"] = credType
	record.Metadata["channel_index"] = index
	record.Metadata["prefix"] = pinned
	record.Metadata["published"] = ch.Published
	if _, ok := record.Metadata["created_by"]; !ok {
		record.Metadata["created_by"] = actor.Username
	}
	record.Prefix = pinned
	return nil
}

// findChannelCredentialIndex returns the existing channel index of the
// credential this record refers to, matched by file base name within the same
// channel and credential type. Returns 0 when the credential is new.
func (h *Handler) findChannelCredentialIndex(ch multiuser.Channel, credType string, record *coreauth.Auth) int {
	if h.authManager == nil {
		return 0
	}
	base := record.FileName
	if base == "" {
		base = record.ID
	}
	base = filepath.Base(strings.TrimPrefix(base, ch.Prefix+"-"))
	if base == "" {
		return 0
	}
	for _, existing := range h.authManager.List() {
		if existing == nil || existing.ChannelID() != ch.ID {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(existing.Provider), credType) {
			continue
		}
		if name, _ := existing.Metadata["provider_name"].(string); strings.TrimSpace(name) != "" {
			continue
		}
		name := existing.FileName
		if name == base || strings.HasSuffix(name, "-"+base) {
			if index, ok := hubMetadataInt(existing.Metadata, "channel_index"); ok && index > 0 {
				return index
			}
		}
	}
	return 0
}

func (h *Handler) applyHubCredentialContext(ctx context.Context, record *coreauth.Auth) error {
	value, ok := multiuser.CredentialContextFrom(ctx)
	if !ok {
		return nil
	}
	if h.hubStore == nil {
		return multiuser.ErrDenied
	}
	actor, ok := h.hubStore.User(value.Actor.Username)
	if !ok || actor.Disabled {
		return multiuser.ErrDenied
	}
	ch, ok := h.hubStore.Channel(value.Channel.ID)
	if !ok || (ch.Owner != actor.Username && actor.Role != "admin" && !ch.Public) {
		return multiuser.ErrDenied
	}
	if errStamp := h.stampHubCredentialChannel(actor, ch, record); errStamp != nil {
		return errStamp
	}
	pinned := record.Prefix
	name := record.FileName
	if name == "" {
		name = record.ID
	}
	if name == "" {
		name = uuid.NewString() + ".json"
	}
	name = filepath.Base(name)
	// Strip any previously applied pool or pinned prefix before applying the
	// current pinned prefix, so re-saves never double-prefix the file name.
	if channelPrefix, _ := record.Metadata["channel_prefix"].(string); channelPrefix != "" {
		name = strings.TrimPrefix(name, channelPrefix+"-")
	}
	if !strings.HasPrefix(name, pinned+"-") {
		name = pinned + "-" + name
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		name += ".json"
	}
	if h.authManager == nil {
		return fmt.Errorf("credential manager unavailable")
	}
	if existing, ok := h.authManager.GetByID(h.authIDForPath(filepath.Join(h.cfg.AuthDir, name))); ok && existing.ChannelID() != ch.ID {
		return multiuser.ErrDenied
	}
	record.ID = h.authIDForPath(filepath.Join(h.cfg.AuthDir, name))
	record.FileName = name
	if record.Attributes != nil {
		delete(record.Attributes, coreauth.AttributePath)
		delete(record.Attributes, coreauth.AttributeSource)
	}
	return nil
}

func (h *Handler) HubUploadCredential(c *gin.Context) {
	ch, ok := h.hubChannel(c, c.Query("channel_id"))
	if !ok {
		return
	}
	c.Set("hubAction", "credential.upload")
	c.Set("hubTarget", ch.ID)
	if h.authManager == nil {
		c.JSON(503, gin.H{"error": "credential manager unavailable"})
		return
	}
	var data []byte
	name := c.Query("name")
	if c.ContentType() == "multipart/form-data" {
		file, err := c.FormFile("file")
		if err != nil {
			hubError(c, fmt.Errorf("%w: file is required", multiuser.ErrInvalid))
			return
		}
		name = file.Filename
		src, errOpen := file.Open()
		if errOpen != nil {
			hubError(c, errOpen)
			return
		}
		var errRead error
		data, errRead = io.ReadAll(src)
		errClose := src.Close()
		if errRead != nil {
			hubError(c, errRead)
			return
		}
		if errClose != nil {
			hubError(c, errClose)
			return
		}
	} else {
		var errRead error
		data, errRead = io.ReadAll(c.Request.Body)
		if errRead != nil {
			hubError(c, errRead)
			return
		}
	}
	if isUnsafeAuthFileName(name) || !strings.HasSuffix(strings.ToLower(name), ".json") {
		hubError(c, fmt.Errorf("%w: name must be a .json filename", multiuser.ErrInvalid))
		return
	}
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil || metadata == nil {
		hubError(c, fmt.Errorf("%w: invalid credential JSON", multiuser.ErrInvalid))
		return
	}
	// Recognized provider-specific shapes without an explicit type (e.g. the
	// Kiro social export) are normalized before the provider check.
	coreauth.NormalizeCredentialMetadata(metadata)
	provider, _ := metadata["type"].(string)
	if strings.TrimSpace(provider) == "" {
		hubError(c, fmt.Errorf("%w: credential type is required", multiuser.ErrInvalid))
		return
	}
	disabled, _ := metadata["disabled"].(bool)
	record := &coreauth.Auth{ID: name, FileName: name, Provider: provider, Metadata: metadata, Disabled: disabled, Status: coreauth.StatusActive}
	if disabled {
		record.Status = coreauth.StatusDisabled
	}
	ctx := multiuser.WithCredentialContext(c.Request.Context(), multiuser.CredentialContext{Actor: multiuser.CurrentUser(c), Channel: ch})
	path, err := h.saveTokenRecord(ctx, record)
	if err != nil {
		hubError(c, err)
		return
	}
	a, err := h.buildAuthFromFileData(path, nil)
	if err == nil {
		err = h.upsertAuthRecord(ctx, a)
	}
	if err != nil {
		hubError(c, err)
		return
	}
	if h.postAuthPersistHook != nil {
		if err := h.postAuthPersistHook(ctx, a); err != nil {
			hubError(c, err)
			return
		}
	}
	c.Set("hubTarget", a.ID)
	c.JSON(201, gin.H{"status": "ok", "credential_id": a.ID})
}

func (h *Handler) HubCredentials(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(503, gin.H{"error": "credential manager unavailable"})
		return
	}
	out := make([]gin.H, 0)
	user := multiuser.CurrentUser(c)
	for _, a := range h.authManager.List() {
		if owner := c.Query("owner"); owner != "" && a.Owner() != owner {
			continue
		}
		if channel := c.Query("channel_id"); channel != "" && a.ChannelID() != channel {
			continue
		}
		_, status, _, retry := reconcileAuthFileCooldownState(a, time.Now().UTC())
		kind := hubCredentialKind(a)
		manageable := a.Owner() == user.Username || user.Role == "admin" || h.hubStore.CanManageChannel(user, a.ChannelID())
		channelSlug := ""
		if ch, okChannel := h.hubStore.Channel(a.ChannelID()); okChannel {
			channelSlug = ch.Slug
		}
		item := gin.H{"id": a.ID, "route_id": base64.RawURLEncoding.EncodeToString([]byte(a.ID)), "name": a.FileName, "label": a.Label, "provider": a.Provider, "kind": kind, "owner": a.Owner(), "created_by": a.Metadata["created_by"], "channel_id": a.ChannelID(), "channel": a.Prefix, "channel_prefix": a.Metadata["channel_prefix"], "channel_slug": channelSlug, "published": a.ChannelPublished(), "disabled": a.Disabled, "status": status, "next_retry_after": retry, "quota": quotaObservationPayloadForProvider(a.Provider, a.Quota), "model_quotas": modelQuotaObservationPayload(a.Provider, a.ModelStates), "manageable": manageable, "claimable": a.Owner() == "" && user.Role == "admin" && !coreauth.IsConfigAPIKeyAuth(a)}
		if index, okIndex := hubMetadataInt(a.Metadata, "channel_index"); okIndex {
			item["channel_index"] = index
		}
		if kind == "provider" {
			item["base_url"], _ = a.Metadata["base_url"].(string)
			item["provider_name"], _ = a.Metadata["provider_name"].(string)
			if models, okModels := a.Metadata["models"]; okModels {
				item["provider_models"] = models
			}
			item["proxy_url"], _ = a.Metadata["proxy_url"].(string)
		}
		entry := h.buildAuthFileEntry(a)
		for _, key := range []string{"auth_index", "email", "project_id", "id_token", "created_at", "updated_at", "modtime", "last_refresh", "size", "unavailable", "status_message", "cooldowns", "recent_requests", "success", "failed", "note", "priority", "weight"} {
			if value, ok := entry[key]; ok {
				item[key] = value
			}
		}
		models := make([]string, 0)
		definitions := make([]gin.H, 0)
		for _, m := range registry.GetGlobalRegistry().GetModelsForClient(a.ID) {
			if m != nil {
				models = append(models, m.ID)
				name := m.MetadataModelID
				if name == "" {
					name = strings.TrimPrefix(m.ID, a.Prefix+"/")
				}
				definitions = append(definitions, gin.H{"id": m.ID, "name": name})
			}
		}
		item["models"] = models
		if manageable && kind != "provider" {
			item["model_definitions"] = definitions
			if modelConfig, configured := a.CredentialModels(); configured {
				item["model_config"] = modelConfig
			}
			item["model_management_supported"] = coreauth.SupportsCredentialModels(a)
			item["model_aliases"] = a.Metadata["model_aliases"]
			if _, exists := a.Metadata["model_aliases"]; !exists {
				item["model_aliases"] = a.Metadata["model-aliases"]
			}
		}
		if enabled, ok := a.Metadata["enabled_models"]; ok {
			item["enabled_models"] = enabled
		}
		out = append(out, item)
	}
	c.JSON(200, gin.H{"credentials": out})
}

func (h *Handler) HubDownloadCredential(c *gin.Context) {
	a, ok := h.hubCredential(c, true)
	if !ok {
		return
	}
	path := a.Attributes[coreauth.AttributePath]
	if path == "" {
		hubError(c, multiuser.ErrNotFound)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		hubError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", a.FileName))
	c.Data(http.StatusOK, "application/json", data)
}

func (h *Handler) HubPatchCredentialFields(c *gin.Context) {
	a, ok := h.hubCredential(c, true)
	if !ok {
		return
	}
	var fields map[string]json.RawMessage
	if err := c.ShouldBindJSON(&fields); err != nil {
		hubError(c, multiuser.ErrInvalid)
		return
	}
	for key := range fields {
		switch rootAuthFileField(key) {
		case "proxy_url", "headers", "priority", "weight", "disable_cooling", "disable-cooling", "websockets", "using_api", "note", "excluded_models", "excluded-models", "expired", "request_retry", "model_aliases", "request_scoped_errors":
		default:
			hubError(c, fmt.Errorf("%w: field %s cannot be changed from the account console", multiuser.ErrInvalid, key))
			return
		}
	}
	fields["name"], _ = json.Marshal(a.ID)
	body, err := json.Marshal(fields)
	if err != nil {
		hubError(c, err)
		return
	}
	c.Request.Body = io.NopCloser(strings.NewReader(string(body)))
	c.Set("hubAction", "credential.fields.update")
	h.PatchAuthFileFields(c)
}

func (h *Handler) HubRefreshCredential(c *gin.Context) {
	a, ok := h.hubCredential(c, true)
	if !ok {
		return
	}
	c.Set("hubAction", "credential.refresh")
	if _, err := h.authManager.ForceRefreshAuth(c.Request.Context(), a.ID); err != nil {
		hubError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) HubPatchCredential(c *gin.Context) {
	a, ok := h.hubCredential(c, true)
	if !ok {
		return
	}
	c.Set("hubAction", "credential.update")
	if coreauth.IsConfigAPIKeyAuth(a) {
		hubError(c, fmt.Errorf("%w: config-backed credentials are managed through server config", multiuser.ErrInvalid))
		return
	}
	var body struct {
		Disabled  *bool     `json:"disabled"`
		Label     *string   `json:"label"`
		Note      *string   `json:"note"`
		Models    *[]string `json:"enabled_models"`
		AllModels bool      `json:"all_models"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		hubError(c, multiuser.ErrInvalid)
		return
	}
	if body.Disabled == nil && body.Label == nil && body.Note == nil && body.Models == nil && !body.AllModels {
		hubError(c, multiuser.ErrInvalid)
		return
	}
	if a.Metadata == nil {
		a.Metadata = map[string]any{}
	}
	if body.Disabled != nil {
		a.Disabled = *body.Disabled
		a.Metadata["disabled"] = a.Disabled
		if a.Disabled {
			a.Status = coreauth.StatusDisabled
		} else {
			a.Status = coreauth.StatusActive
		}
	}
	if body.Label != nil {
		a.Label = strings.TrimSpace(*body.Label)
		a.Metadata["label"] = a.Label
	}
	if body.Note != nil {
		a.Metadata["note"] = strings.TrimSpace(*body.Note)
	}
	if body.AllModels {
		delete(a.Metadata, "enabled_models")
	} else if body.Models != nil {
		for _, model := range *body.Models {
			if strings.TrimSpace(model) == "" || strings.HasPrefix(model, a.Prefix+"/") {
				hubError(c, fmt.Errorf("%w: enabled_models uses model names without the channel prefix", multiuser.ErrInvalid))
				return
			}
		}
		a.Metadata["enabled_models"] = *body.Models
	}
	if err := h.persistHubCredential(c.Request.Context(), a); err != nil {
		hubError(c, err)
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

func (h *Handler) persistHubCredential(ctx context.Context, a *coreauth.Auth) error {
	store := h.tokenStoreWithBaseDir()
	if store == nil {
		return fmt.Errorf("token store unavailable")
	}
	if _, err := store.Save(ctx, a); err != nil {
		return err
	}
	updated, err := h.authManager.Update(ctx, a)
	if err != nil {
		return err
	}
	if h.postAuthPersistHook != nil {
		if updated == nil {
			updated = a
		}
		return h.postAuthPersistHook(ctx, updated)
	}
	return nil
}

func (h *Handler) HubDeleteCredential(c *gin.Context) {
	a, ok := h.hubCredential(c, true)
	if !ok {
		return
	}
	c.Set("hubAction", "credential.delete")
	if coreauth.IsConfigAPIKeyAuth(a) {
		hubError(c, fmt.Errorf("%w: config-backed credentials are managed through server config", multiuser.ErrInvalid))
		return
	}
	if _, status, err := h.deleteAuthFileByName(c.Request.Context(), a.FileName); err != nil {
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

func (h *Handler) HubClaimCredential(c *gin.Context) {
	if multiuser.CurrentUser(c).Role != "admin" {
		hubError(c, multiuser.ErrDenied)
		return
	}
	a, ok := h.hubCredential(c, true)
	if !ok {
		return
	}
	c.Set("hubAction", "credential.claim")
	if a.Owner() != "" || coreauth.IsConfigAPIKeyAuth(a) {
		hubError(c, multiuser.ErrConflict)
		return
	}
	var body struct {
		ChannelID string `json:"channel_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		hubError(c, multiuser.ErrInvalid)
		return
	}
	ch, ok := h.hubChannel(c, body.ChannelID)
	if !ok {
		return
	}
	if errStamp := h.stampHubCredentialChannel(multiuser.CurrentUser(c), ch, a); errStamp != nil {
		hubError(c, errStamp)
		return
	}
	if err := h.persistHubCredential(c.Request.Context(), a); err != nil {
		hubError(c, err)
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

func (h *Handler) HubPatchChannel(c *gin.Context) {
	ch, ok := h.hubChannel(c, c.Param("id"))
	if !ok {
		return
	}
	var body struct {
		Published    *bool   `json:"published"`
		Announcement *string `json:"announcement"`
		Public       *bool   `json:"public"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || (body.Published == nil && body.Announcement == nil && body.Public == nil) {
		hubError(c, multiuser.ErrInvalid)
		return
	}
	if body.Announcement != nil {
		c.Set("hubAction", "channel.announcement")
	} else {
		c.Set("hubAction", "channel.update")
	}
	c.Set("hubTarget", ch.ID)
	updated, err := h.hubStore.UpdateChannel(multiuser.CurrentUser(c), ch.ID, body.Published, body.Announcement, body.Public)
	if err != nil {
		hubError(c, err)
		return
	}
	// Publication changes affect credentials; pinned routing prefixes stay unchanged.
	if body.Published != nil && h.authManager != nil {
		for _, a := range h.authManager.List() {
			if a.ChannelID() == ch.ID {
				a.Metadata["published"] = updated.Published
				if err := h.persistHubCredential(c.Request.Context(), a); err != nil {
					hubError(c, err)
					return
				}
			}
		}
	}
	c.JSON(200, gin.H{"channel": updated})
}

// HubDeleteChannel removes an empty channel. Credentials and providers must be
// detached or deleted first so their pinned model IDs never silently lose
// routing; board messages are removed with the channel.
func (h *Handler) HubDeleteChannel(c *gin.Context) {
	ch, ok := h.hubStore.Channel(c.Param("id"))
	if !ok {
		hubError(c, multiuser.ErrNotFound)
		return
	}
	c.Set("hubAction", "channel.delete")
	c.Set("hubTarget", ch.ID)
	if h.authManager != nil {
		for _, a := range h.authManager.List() {
			if a.ChannelID() == ch.ID {
				hubError(c, fmt.Errorf("%w: channel still has mounted credentials or providers", multiuser.ErrConflict))
				return
			}
		}
	}
	if err := h.hubStore.DeleteChannel(multiuser.CurrentUser(c), ch.ID); err != nil {
		hubError(c, err)
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

func (h *Handler) HubOAuth(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	switch provider {
	case "codex", "claude", "kiro", "kimi", "kimi-ai", "qoder", "qoder-cn", "antigravity", "xai", "meta", "devin", "zcode":
	default:
		hubError(c, fmt.Errorf("%w: use a built-in OAuth provider or upload a credential JSON", multiuser.ErrInvalid))
		return
	}
	ch, ok := h.hubChannel(c, c.Query("channel_id"))
	if !ok {
		return
	}
	c.Set("hubAction", "credential.oauth.start")
	c.Set("hubTarget", ch.ID)
	ctx := multiuser.WithCredentialContext(c.Request.Context(), multiuser.CredentialContext{Actor: multiuser.CurrentUser(c), Channel: ch})
	c.Request = c.Request.WithContext(ctx)
	h.StartOAuthV8(c)
}

func (h *Handler) HubMarketplace(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(503, gin.H{"error": "credential manager unavailable"})
		return
	}
	available := map[string]bool{}
	for _, m := range registry.GetGlobalRegistry().GetAvailableModels("openai") {
		if id, ok := m["id"].(string); ok {
			available[id] = true
		}
	}
	channels := map[string]multiuser.Channel{}
	for _, ch := range h.hubStore.Channels() {
		channels[ch.ID] = ch
	}
	items := map[string]gin.H{}
	for _, a := range h.authManager.List() {
		if !a.ChannelPublished() {
			continue
		}
		if owner := c.Query("owner"); owner != "" && a.Owner() != owner {
			continue
		}
		if provider := c.Query("provider"); provider != "" && a.Provider != provider {
			continue
		}
		channelSlug := ""
		if ch, okChannel := channels[a.ChannelID()]; okChannel {
			channelSlug = ch.Slug
		}
		kind := hubCredentialKind(a)
		for _, m := range registry.GetGlobalRegistry().GetModelsForClient(a.ID) {
			if m == nil || !strings.HasPrefix(m.ID, a.Prefix+"/") {
				continue
			}
			if query := strings.ToLower(c.Query("q")); query != "" && !strings.Contains(strings.ToLower(m.ID+" "+m.DisplayName+" "+channelSlug), query) {
				continue
			}
			item := items[m.ID]
			if item == nil {
				item = gin.H{"id": m.ID, "base_model": strings.TrimPrefix(m.ID, a.Prefix+"/"), "display_name": m.DisplayName, "description": m.Description, "owner": a.Owner(), "created_by": a.Metadata["created_by"], "channel_id": a.ChannelID(), "channel": a.Prefix, "channel_slug": channelSlug, "provider": a.Provider, "kind": kind, "credential_label": a.Label, "available": available[m.ID], "credential_count": 0, "context_length": m.ContextLength, "max_completion_tokens": m.MaxCompletionTokens, "thinking": m.Thinking}
				if index, okIndex := hubMetadataInt(a.Metadata, "channel_index"); okIndex {
					item["channel_index"] = index
				}
				items[m.ID] = item
			}
			item["credential_count"] = item["credential_count"].(int) + 1
		}
	}
	out := make([]gin.H, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["id"].(string) < out[j]["id"].(string) })
	c.JSON(200, gin.H{"models": out, "channels": h.hubStore.Channels()})
}

// HubMarketplacePerformance returns trailing-24h usage aggregates per pinned
// model ID so the marketplace can show TPS, latency, TTFT and success-rate
// trends without each client rescanning the usage journal.
func (h *Handler) HubMarketplacePerformance(c *gin.Context) {
	if err := h.hubStore.JournalReady(); err != nil {
		hubError(c, err)
		return
	}
	perf, err := h.hubStore.ModelPerformanceContext(c.Request.Context(), time.Now())
	if err != nil {
		hubError(c, err)
		return
	}
	out := make([]*multiuser.ModelPerformance, 0, len(perf))
	for _, entry := range perf {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	c.JSON(200, gin.H{"models": out})
}
