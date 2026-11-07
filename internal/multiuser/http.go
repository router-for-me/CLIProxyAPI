package multiuser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

const userContextKey = "hubUser"

type Controller struct {
	Store *Store
}

func CurrentUser(c *gin.Context) User {
	value, _ := c.Get(userContextKey)
	user, _ := value.(User)
	return user
}

func (h *Controller) Session() gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := h.Store.AuthenticateSession(bearer(c.GetHeader("Authorization")))
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "please log in"})
			return
		}
		c.Set(userContextKey, user)
		c.Next()
	}
}

func (h *Controller) Admin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if CurrentUser(c).Role != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "administrator access required"})
			return
		}
		c.Next()
	}
}

func replyError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, context.Canceled):
		c.Status(499)
		return
	case errors.Is(err, ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, ErrDenied):
		status = http.StatusForbidden
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, ErrJournalBuilding):
		c.Header("Retry-After", "2")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error(), "code": "journal_index_building"})
		return
	}
	if status == 500 {
		log.WithError(err).Error("multi-user: operation failed")
		c.JSON(status, gin.H{"error": "operation could not be saved"})
		return
	}
	c.JSON(status, gin.H{"error": err.Error()})
}

func bind(c *gin.Context, body any) bool {
	if err := c.ShouldBindJSON(body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return false
	}
	return true
}

func bindIdentity(c *gin.Context, body any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return false
	}
	return true
}

func (h *Controller) record(c *gin.Context, action, target string) {
	c.Set("hubAction", action)
	c.Set("hubTarget", target)
}

func (h *Controller) AuditMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		action := c.GetString("hubAction")
		if action == "" {
			if c.Request.Method == "GET" || c.Request.Method == "HEAD" {
				return
			}
			action = c.Request.Method + " " + c.FullPath()
		}
		actor := CurrentUser(c).Username
		if actor == "" {
			actor = c.GetString("hubActor")
		}
		if actor == "" {
			actor = "anonymous"
		}
		target := c.GetString("hubTarget")
		if target == "" {
			target = c.Param("id")
		}
		if err := h.Store.Audit(AuditEvent{Actor: actor, Action: action, Target: target, Status: c.Writer.Status(), IP: c.ClientIP()}); err != nil {
			log.WithError(err).Error("multi-user: failed to persist audit")
		}
	}
}

func (h *Controller) RegisterRoutes(public, member *gin.RouterGroup) {
	public.GET("/status", func(c *gin.Context) { c.JSON(200, gin.H{"needs_setup": h.Store.NeedsSetup()}) })
	public.POST("/register", h.register)
	public.POST("/login", h.login)
	member.GET("/me", func(c *gin.Context) { c.JSON(200, gin.H{"user": CurrentUser(c)}) })
	member.POST("/logout", func(c *gin.Context) {
		h.record(c, "account.logout", CurrentUser(c).Username)
		if err := h.Store.Logout(bearer(c.GetHeader("Authorization"))); err != nil {
			replyError(c, err)
			return
		}
		c.JSON(200, gin.H{"status": "ok"})
	})
	member.POST("/me/password", h.password)
	member.GET("/keys", func(c *gin.Context) { c.JSON(200, gin.H{"keys": h.Store.Keys(CurrentUser(c))}) })
	member.GET("/keys/all", func(c *gin.Context) {
		// Usage filters need every member's key name; never expose key hints here.
		type keyRef struct {
			ID    string `json:"id"`
			Owner string `json:"owner"`
			Name  string `json:"name"`
		}
		keys := h.Store.KeysAll()
		out := make([]keyRef, 0, len(keys))
		for _, key := range keys {
			out = append(out, keyRef{ID: key.ID, Owner: key.Owner, Name: key.Name})
		}
		c.JSON(200, gin.H{"keys": out})
	})
	member.POST("/keys", h.createKey)
	member.PATCH("/keys/:id", h.updateKey)
	member.DELETE("/keys/:id", h.deleteKey)
	member.GET("/channels", func(c *gin.Context) { c.JSON(200, gin.H{"channels": h.Store.Channels()}) })
	member.POST("/channels", h.createChannel)
	member.GET("/channels/:id/messages", h.boardMessages)
	member.POST("/channels/:id/messages", h.postBoardMessage)
	member.DELETE("/channels/:id/messages/:mid", h.deleteBoardMessage)
	member.GET("/users", func(c *gin.Context) { c.JSON(200, gin.H{"users": h.Store.Users()}) })
	member.PATCH("/users/:id", h.Admin(), h.updateUser)
	member.GET("/invites", h.Admin(), h.invites)
	member.POST("/invites", h.Admin(), h.createInvite)
	member.DELETE("/invites/:id", h.Admin(), h.deleteInvite)
	member.GET("/usage", h.usage)
	member.GET("/calls", h.calls)
	member.GET("/usage/summary", h.summary)
	member.GET("/usage/attempts", h.usageAttempts)
	member.GET("/usage/facets", h.usageFacets)
	member.GET("/audit", h.audits)
}

func (h *Controller) register(c *gin.Context) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Invite   string `json:"invite"`
	}
	if !bindIdentity(c, &body) {
		return
	}
	h.record(c, "account.register", strings.ToLower(strings.TrimSpace(body.Username)))
	user, err := h.Store.Register(body.Username, body.Password, body.Invite)
	if err != nil {
		replyError(c, err)
		return
	}
	c.Set(userContextKey, user)
	c.JSON(http.StatusCreated, gin.H{"user": user})
}

func (h *Controller) login(c *gin.Context) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !bind(c, &body) {
		return
	}
	h.record(c, "account.login", strings.ToLower(strings.TrimSpace(body.Username)))
	user, token, err := h.Store.Login(body.Username, body.Password)
	if err != nil {
		if errors.Is(err, ErrDenied) {
			c.JSON(401, gin.H{"error": "invalid username or password"})
			return
		}
		replyError(c, err)
		return
	}
	c.Set(userContextKey, user)
	c.JSON(200, gin.H{"user": user, "token": token})
}

func (h *Controller) password(c *gin.Context) {
	var body struct {
		Current  string `json:"current_password"`
		Password string `json:"password"`
	}
	if !bind(c, &body) {
		return
	}
	h.record(c, "account.password", CurrentUser(c).Username)
	if err := h.Store.ChangePassword(CurrentUser(c), body.Current, body.Password); err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, gin.H{"status": "ok", "logged_out": true})
}

func (h *Controller) createKey(c *gin.Context) {
	var body struct {
		Name string `json:"name"`
	}
	if !bind(c, &body) {
		return
	}
	key, secret, err := h.Store.CreateKey(CurrentUser(c), body.Name)
	if err != nil {
		replyError(c, err)
		return
	}
	h.record(c, "key.create", key.ID)
	c.JSON(201, gin.H{"key": key, "secret": secret})
}

func (h *Controller) updateKey(c *gin.Context) {
	var body struct {
		Name          *string   `json:"name"`
		Disabled      *bool     `json:"disabled"`
		AutoBalance   *bool     `json:"auto_balance"`
		BlockedModels *[]string `json:"blocked_models"`
	}
	if !bind(c, &body) {
		return
	}
	h.record(c, "key.update", c.Param("id"))
	if body.Name == nil && body.Disabled == nil && body.AutoBalance == nil && body.BlockedModels == nil {
		replyError(c, ErrInvalid)
		return
	}
	if err := h.Store.UpdateKey(CurrentUser(c), c.Param("id"), body.Name, body.Disabled, body.AutoBalance, body.BlockedModels, false); err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

func (h *Controller) deleteKey(c *gin.Context) {
	h.record(c, "key.revoke", c.Param("id"))
	if err := h.Store.UpdateKey(CurrentUser(c), c.Param("id"), nil, nil, nil, nil, true); err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

func (h *Controller) createChannel(c *gin.Context) {
	var body struct {
		Slug   string `json:"slug"`
		Public bool   `json:"public"`
	}
	if !bindIdentity(c, &body) {
		return
	}
	channel, err := h.Store.CreateChannel(CurrentUser(c), body.Slug, body.Public)
	if err != nil {
		replyError(c, err)
		return
	}
	h.record(c, "channel.create", channel.ID)
	c.JSON(201, gin.H{"channel": channel})
}

func (h *Controller) boardMessages(c *gin.Context) {
	channelID := c.Param("id")
	if _, ok := h.Store.Channel(channelID); !ok {
		replyError(c, ErrNotFound)
		return
	}
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			replyError(c, ErrInvalid)
			return
		}
		limit = value
	}
	c.JSON(200, gin.H{"messages": h.Store.ListBoardMessages(channelID, limit, c.Query("before_id"))})
}

func (h *Controller) postBoardMessage(c *gin.Context) {
	var body struct {
		Content string `json:"content"`
	}
	if !bind(c, &body) {
		return
	}
	h.record(c, "board.post", c.Param("id"))
	message, err := h.Store.AddBoardMessage(CurrentUser(c), c.Param("id"), body.Content)
	if err != nil {
		replyError(c, err)
		return
	}
	c.JSON(201, gin.H{"message": message})
}

func (h *Controller) deleteBoardMessage(c *gin.Context) {
	h.record(c, "board.delete", c.Param("mid"))
	if err := h.Store.DeleteBoardMessage(CurrentUser(c), c.Param("id"), c.Param("mid")); err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

func (h *Controller) updateUser(c *gin.Context) {
	var body struct {
		Disabled *bool   `json:"disabled"`
		Role     *string `json:"role"`
	}
	if !bindIdentity(c, &body) {
		return
	}
	h.record(c, "user.update", c.Param("id"))
	if body.Disabled == nil && body.Role == nil {
		replyError(c, ErrInvalid)
		return
	}
	if err := h.Store.UpdateUser(CurrentUser(c), c.Param("id"), body.Disabled, body.Role); err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

func (h *Controller) invites(c *gin.Context) {
	items, err := h.Store.Invites(CurrentUser(c))
	if err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, gin.H{"invites": items})
}

func (h *Controller) createInvite(c *gin.Context) {
	var body struct {
		MaxUses int `json:"max_uses"`
		Days    int `json:"days"`
	}
	if !bind(c, &body) {
		return
	}
	if body.MaxUses == 0 {
		body.MaxUses = 1
	}
	if body.Days == 0 {
		body.Days = 7
	}
	invite, err := h.Store.CreateInvite(CurrentUser(c), body.MaxUses, body.Days)
	if err != nil {
		replyError(c, err)
		return
	}
	h.record(c, "invite.create", invite.ID)
	c.JSON(201, gin.H{"invite": invite})
}

func (h *Controller) deleteInvite(c *gin.Context) {
	h.record(c, "invite.revoke", c.Param("id"))
	if err := h.Store.RevokeInvite(CurrentUser(c), c.Param("id")); err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, gin.H{"status": "ok"})
}

func queryFilter(c *gin.Context) (Filter, error) {
	f := Filter{Consumer: c.Query("consumer"), Owner: c.Query("owner"), ChannelID: c.Query("channel_id"), CredentialID: c.Query("credential_id"), KeyID: c.Query("key_id"), Model: c.Query("model"), Outcome: c.Query("outcome"), Provider: c.Query("provider"), Stream: c.Query("stream"), Status: c.Query("status"), Endpoint: c.Query("endpoint"), Effort: c.Query("effort"), Balanced: c.Query("balanced"), Sort: c.Query("sort"), Actor: c.Query("actor"), Action: c.Query("action"), Page: 1, PageSize: 50}
	for name, dest := range map[string]*int{"page": &f.Page, "page_size": &f.PageSize} {
		if raw := c.Query(name); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > 1000000 || (name == "page_size" && value > 200) {
				return f, ErrInvalid
			}
			*dest = value
		}
	}
	for name, dest := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if raw := c.Query(name); raw != "" {
			value, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return f, ErrInvalid
			}
			*dest = value
		}
	}
	for value, allowed := range map[string][]string{
		f.Outcome:  {"ok", "failed"},
		f.Stream:   {"stream", "nonstream"},
		f.Balanced: {"yes", "no"},
		f.Sort:     {"time", "tokens", "latency"},
	} {
		if value != "" && !slices.Contains(allowed, value) {
			return f, ErrInvalid
		}
	}
	if _, _, ok := statusRange(f.Status); f.Status != "" && !ok {
		return f, ErrInvalid
	}
	if !f.From.IsZero() && !f.To.IsZero() && !f.To.After(f.From) {
		return f, ErrInvalid
	}
	return f, nil
}

func (h *Controller) historyReady(c *gin.Context) bool {
	if err := h.Store.JournalReady(); err != nil {
		replyError(c, err)
		return false
	}
	return true
}

func (h *Controller) usage(c *gin.Context) {
	f, err := queryFilter(c)
	if err != nil {
		replyError(c, err)
		return
	}
	if !h.historyReady(c) {
		return
	}
	items, total, err := h.Store.UsageContext(c.Request.Context(), f)
	if err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": f.Page, "page_size": f.PageSize})
}
func (h *Controller) usageFacets(c *gin.Context) {
	f, err := queryFilter(c)
	if err != nil {
		replyError(c, err)
		return
	}
	if !h.historyReady(c) {
		return
	}
	facets, err := h.Store.UsageFacetsContext(c.Request.Context(), f)
	if err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, facets)
}

func (h *Controller) usageAttempts(c *gin.Context) {
	trace := strings.TrimSpace(c.Query("trace"))
	if trace == "" {
		replyError(c, ErrInvalid)
		return
	}
	if !h.historyReady(c) {
		return
	}
	items, err := h.Store.UsageAttemptsContext(c.Request.Context(), trace)
	if err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items})
}

func (h *Controller) calls(c *gin.Context) {
	f, err := queryFilter(c)
	if err != nil {
		replyError(c, err)
		return
	}
	if !h.historyReady(c) {
		return
	}
	items, total, err := h.Store.CallsContext(c.Request.Context(), f)
	if err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": f.Page, "page_size": f.PageSize})
}
func (h *Controller) summary(c *gin.Context) {
	f, err := queryFilter(c)
	if err != nil {
		replyError(c, err)
		return
	}
	if !h.historyReady(c) {
		return
	}
	result, err := h.Store.SummaryResultContext(c.Request.Context(), f)
	if err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, gin.H{"total": result.Total, "groups": result.Groups, "buckets": result.Buckets})
}
func (h *Controller) audits(c *gin.Context) {
	f, err := queryFilter(c)
	if err != nil {
		replyError(c, err)
		return
	}
	if !h.historyReady(c) {
		return
	}
	items, total, err := h.Store.AuditsContext(c.Request.Context(), f)
	if err != nil {
		replyError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": f.Page, "page_size": f.PageSize})
}
