package management

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	guestKeyPrefix      = "cpg_"
	guestStoreFileName  = "guest-keys.json"
	guestPrincipalKey   = "management.guest-principal"
	maxGuestNameLength  = 64
	managementV8Prefix  = "/v8/management"
	managementV0Prefix  = "/v0/management"
	guestAccessDeniedMs = "guest access denied"

	// PluginRouteContextKey marks a request that is dispatched to a plugin-owned
	// Management API route. Guests with plugin-routes access may call those routes.
	PluginRouteContextKey = "management.plugin-route"
)

// GuestLevel is the access a guest has on one route group.
type GuestLevel string

const (
	GuestLevelNone GuestLevel = "none"
	GuestLevelView GuestLevel = "view"
	GuestLevelEdit GuestLevel = "edit"
)

func (l GuestLevel) rank() int {
	switch l {
	case GuestLevelEdit:
		return 2
	case GuestLevelView:
		return 1
	default:
		return 0
	}
}

// guestRecord is one stored guest key. Only the SHA-256 of the key is kept.
type guestRecord struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	KeyHash     string                `json:"key-hash"`
	CreatedAt   time.Time             `json:"created-at"`
	Permissions map[string]GuestLevel `json:"permissions"`
}

func (g *guestRecord) level(group string) GuestLevel {
	if g == nil {
		return GuestLevelNone
	}
	if level, ok := g.Permissions[group]; ok {
		return level
	}
	return GuestLevelNone
}

// fullPermissions returns a level for every route group, defaulting to none.
func (g *guestRecord) fullPermissions() map[string]GuestLevel {
	out := make(map[string]GuestLevel, len(guestRouteGroups))
	for _, group := range guestRouteGroups {
		out[group.ID] = g.level(group.ID)
	}
	return out
}

type guestView struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	CreatedAt   time.Time             `json:"created-at"`
	Permissions map[string]GuestLevel `json:"permissions"`
}

func (g *guestRecord) view() guestView {
	return guestView{ID: g.ID, Name: g.Name, CreatedAt: g.CreatedAt, Permissions: g.fullPermissions()}
}

// guestStore persists guest keys next to the config file. An empty path keeps
// them in memory only.
type guestStore struct {
	mu    sync.RWMutex
	path  string
	items []*guestRecord
}

func hashGuestKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func newGuestToken(byteLen int) (string, error) {
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func loadGuestStore(path string) (*guestStore, error) {
	s := &guestStore{path: path}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return s, nil
	}
	var file struct {
		Guests []*guestRecord `json:"guests"`
	}
	if err = json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	s.items = file.Guests
	return s, nil
}

// saveLocked writes the store atomically. The caller holds s.mu.
func (s *guestStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	file := struct {
		Guests []*guestRecord `json:"guests"`
	}{Guests: s.items}
	if file.Guests == nil {
		file.Guests = []*guestRecord{}
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), guestStoreFileName+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err = os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err = os.Rename(tmpName, s.path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func (s *guestStore) lookup(key string) *guestRecord {
	hash := hashGuestKey(key)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var found *guestRecord
	for _, item := range s.items {
		if subtle.ConstantTimeCompare([]byte(hash), []byte(item.KeyHash)) == 1 {
			found = item
		}
	}
	if found == nil {
		return nil
	}
	clone := *found
	clone.Permissions = copyPermissions(found.Permissions)
	return &clone
}

func copyPermissions(in map[string]GuestLevel) map[string]GuestLevel {
	out := make(map[string]GuestLevel, len(in))
	for group, level := range in {
		out[group] = level
	}
	return out
}

func (s *guestStore) list() []guestView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]guestView, 0, len(s.items))
	for _, item := range s.items {
		out = append(out, item.view())
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *guestStore) add(name string, perms map[string]GuestLevel) (*guestRecord, string, error) {
	id, err := newGuestToken(8)
	if err != nil {
		return nil, "", err
	}
	secret, err := newGuestToken(32)
	if err != nil {
		return nil, "", err
	}
	key := guestKeyPrefix + secret
	rec := &guestRecord{
		ID:          id,
		Name:        name,
		KeyHash:     hashGuestKey(key),
		CreatedAt:   time.Now().UTC(),
		Permissions: perms,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, rec)
	if err = s.saveLocked(); err != nil {
		s.items = s.items[:len(s.items)-1]
		return nil, "", err
	}
	return rec, key, nil
}

var errGuestNotFound = errors.New("guest not found")

func (s *guestStore) update(id string, name *string, perms map[string]GuestLevel) (*guestRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.items {
		if item.ID != id {
			continue
		}
		prev := *item
		prev.Permissions = copyPermissions(item.Permissions)
		if name != nil {
			item.Name = *name
		}
		if perms != nil {
			item.Permissions = perms
		}
		if err := s.saveLocked(); err != nil {
			*item = prev
			return nil, err
		}
		return item, nil
	}
	return nil, errGuestNotFound
}

func (s *guestStore) rotate(id string) (*guestRecord, string, error) {
	secret, err := newGuestToken(32)
	if err != nil {
		return nil, "", err
	}
	key := guestKeyPrefix + secret
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.items {
		if item.ID != id {
			continue
		}
		prev := item.KeyHash
		item.KeyHash = hashGuestKey(key)
		if err = s.saveLocked(); err != nil {
			item.KeyHash = prev
			return nil, "", err
		}
		return item, key, nil
	}
	return nil, "", errGuestNotFound
}

func (s *guestStore) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.items {
		if item.ID != id {
			continue
		}
		prev := s.items
		next := make([]*guestRecord, 0, len(s.items)-1)
		next = append(next, s.items[:i]...)
		next = append(next, s.items[i+1:]...)
		s.items = next
		if err := s.saveLocked(); err != nil {
			s.items = prev
			return err
		}
		return nil
	}
	return errGuestNotFound
}

// guests returns the handler's guest store, loading it on first use.
func (h *Handler) guests() *guestStore {
	h.guestsOnce.Do(func() {
		path := ""
		if h.configFilePath != "" {
			path = filepath.Join(filepath.Dir(h.configFilePath), guestStoreFileName)
		}
		store, err := loadGuestStore(path)
		if err != nil {
			// An unreadable store must not silently turn into an empty one that
			// the next save overwrites.
			h.guestsErr = err
			store = &guestStore{path: ""}
		}
		h.guestKeys = store
	})
	return h.guestKeys
}

func parseGuestPermissions(in map[string]string) (map[string]GuestLevel, error) {
	out := make(map[string]GuestLevel, len(guestRouteGroups))
	for group, level := range in {
		if !validGuestRouteGroup(group) {
			return nil, errors.New("unknown route group: " + group)
		}
		switch GuestLevel(level) {
		case GuestLevelNone, GuestLevelView, GuestLevelEdit:
			out[group] = GuestLevel(level)
		default:
			return nil, errors.New("invalid level for " + group + ": " + level)
		}
	}
	for _, group := range guestRouteGroups {
		if _, ok := out[group.ID]; !ok {
			out[group.ID] = GuestLevelNone
		}
	}
	return out, nil
}

func guestFromContext(c *gin.Context) *guestRecord {
	if v, ok := c.Get(guestPrincipalKey); ok {
		if g, ok := v.(*guestRecord); ok {
			return g
		}
	}
	return nil
}

// IsGuestRequest reports whether the request was authenticated with a guest key.
func IsGuestRequest(c *gin.Context) bool {
	return guestFromContext(c) != nil
}

func denyGuest(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": guestAccessDeniedMs})
}

type sessionResponse struct {
	Role        string                `json:"role"`
	Name        string                `json:"name,omitempty"`
	Permissions map[string]GuestLevel `json:"permissions"`
}

// GetSession reports who the caller is and, for guests, what they may do.
func (h *Handler) GetSession(c *gin.Context) {
	if g := guestFromContext(c); g != nil {
		c.JSON(http.StatusOK, sessionResponse{Role: "guest", Name: g.Name, Permissions: g.fullPermissions()})
		return
	}
	perms := make(map[string]GuestLevel, len(guestRouteGroups))
	for _, group := range guestRouteGroups {
		perms[group.ID] = GuestLevelEdit
	}
	c.JSON(http.StatusOK, sessionResponse{Role: "admin", Permissions: perms})
}

func (h *Handler) guestStoreOrFail(c *gin.Context) *guestStore {
	if IsGuestRequest(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": guestAccessDeniedMs})
		return nil
	}
	store := h.guests()
	if h.guestsErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "guest_store_unreadable"})
		return nil
	}
	return store
}

// ListGuestRoutes returns the route groups a guest permission can be set for.
func (h *Handler) ListGuestRoutes(c *gin.Context) {
	if h.guestStoreOrFail(c) == nil {
		return
	}
	c.JSON(http.StatusOK, gin.H{"routes": guestRouteGroups})
}

// ListGuests returns every guest key without its secret.
func (h *Handler) ListGuests(c *gin.Context) {
	store := h.guestStoreOrFail(c)
	if store == nil {
		return
	}
	c.JSON(http.StatusOK, gin.H{"guests": store.list()})
}

type guestWriteBody struct {
	Name        *string           `json:"name"`
	Permissions map[string]string `json:"permissions"`
}

func normalizeGuestName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxGuestNameLength {
		return "", false
	}
	return name, true
}

// CreateGuest creates a guest key. The key is returned once.
func (h *Handler) CreateGuest(c *gin.Context) {
	store := h.guestStoreOrFail(c)
	if store == nil {
		return
	}
	var body guestWriteBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	name := ""
	if body.Name != nil {
		name = *body.Name
	}
	name, ok := normalizeGuestName(name)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_name"})
		return
	}
	perms, err := parseGuestPermissions(body.Permissions)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_permissions", "message": err.Error()})
		return
	}
	rec, key, err := store.add(name, perms)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save_failed"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"guest": rec.view(), "key": key})
}

// PatchGuest renames a guest or changes its permissions.
func (h *Handler) PatchGuest(c *gin.Context) {
	store := h.guestStoreOrFail(c)
	if store == nil {
		return
	}
	var body guestWriteBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	var name *string
	if body.Name != nil {
		n, ok := normalizeGuestName(*body.Name)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_name"})
			return
		}
		name = &n
	}
	var perms map[string]GuestLevel
	if body.Permissions != nil {
		var err error
		perms, err = parseGuestPermissions(body.Permissions)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_permissions", "message": err.Error()})
			return
		}
	}
	rec, err := store.update(c.Param("id"), name, perms)
	if errors.Is(err, errGuestNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"guest": rec.view()})
}

// RotateGuestKey replaces a guest's key. The new key is returned once.
func (h *Handler) RotateGuestKey(c *gin.Context) {
	store := h.guestStoreOrFail(c)
	if store == nil {
		return
	}
	rec, key, err := store.rotate(c.Param("id"))
	if errors.Is(err, errGuestNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"guest": rec.view(), "key": key})
}

// DeleteGuest removes a guest key.
func (h *Handler) DeleteGuest(c *gin.Context) {
	store := h.guestStoreOrFail(c)
	if store == nil {
		return
	}
	err := store.remove(c.Param("id"))
	if errors.Is(err, errGuestNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
