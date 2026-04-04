package management

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/store/accountpool"
)

// requireAccountPool returns false and writes a 501 response if no account pool store is configured.
func (h *Handler) requireAccountPool(c *gin.Context) bool {
	if h.accountPool == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "account pool requires PostgreSQL store (PGSTORE_DSN)"})
		return false
	}
	return true
}

func parseID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return 0, false
	}
	return id, true
}

func parsePagination(c *gin.Context) (int, int) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// --- Members ---

func (h *Handler) ListMembers(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	limit, offset := parsePagination(c)
	members, total, err := h.accountPool.ListMembers(c.Request.Context(),
		c.Query("status"), c.Query("search"), limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": members, "total": total, "limit": limit, "offset": offset})
}

func (h *Handler) GetMember(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	m, err := h.accountPool.GetMember(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if m == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "member not found"})
		return
	}
	c.JSON(http.StatusOK, m)
}

func (h *Handler) CreateMember(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	var m accountpool.Member
	if err := c.ShouldBindJSON(&m); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if m.Email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email is required"})
		return
	}
	if err := h.accountPool.CreateMember(c.Request.Context(), &m); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, m)
}

func (h *Handler) UpdateMember(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var m accountpool.Member
	if err := c.ShouldBindJSON(&m); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	m.ID = id
	if err := h.accountPool.UpdateMember(c.Request.Context(), &m); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, m)
}

func (h *Handler) UpdateMemberStatus(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Status == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status is required"})
		return
	}
	if err := h.accountPool.UpdateMemberStatus(c.Request.Context(), id, body.Status); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) DeleteMember(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := h.accountPool.DeleteMember(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) BatchImportMembers(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	members, parseErrors := parseAccountLines(body.Text)
	var poolMembers []accountpool.Member
	for _, a := range members {
		poolMembers = append(poolMembers, accountpool.Member{
			Email:         a.Email,
			Password:      a.Password,
			RecoveryEmail: a.RecoveryEmail,
			TOTPSecret:    a.TOTPSecret,
			Status:        "available",
		})
	}
	created, dbErrors, err := h.accountPool.BatchCreateMembers(c.Request.Context(), poolMembers)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	allErrors := append(parseErrors, dbErrors...)
	c.JSON(http.StatusOK, gin.H{"created": created, "errors": allErrors, "total_lines": len(members) + len(parseErrors)})
}

func (h *Handler) PickNextAvailableMember(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	m, err := h.accountPool.PickNextAvailableMember(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if m == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no available member"})
		return
	}
	c.JSON(http.StatusOK, m)
}

// --- Leaders ---

func (h *Handler) ListLeaders(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	limit, offset := parsePagination(c)
	leaders, total, err := h.accountPool.ListLeaders(c.Request.Context(),
		c.Query("status"), c.Query("search"), limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": leaders, "total": total, "limit": limit, "offset": offset})
}

func (h *Handler) GetLeader(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	l, err := h.accountPool.GetLeader(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if l == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "leader not found"})
		return
	}
	c.JSON(http.StatusOK, l)
}

func (h *Handler) CreateLeader(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	var l accountpool.Leader
	if err := c.ShouldBindJSON(&l); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if l.Email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email is required"})
		return
	}
	if err := h.accountPool.CreateLeader(c.Request.Context(), &l); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, l)
}

func (h *Handler) UpdateLeader(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var l accountpool.Leader
	if err := c.ShouldBindJSON(&l); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	l.ID = id
	if err := h.accountPool.UpdateLeader(c.Request.Context(), &l); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, l)
}

func (h *Handler) UpdateLeaderStatus(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Status == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status is required"})
		return
	}
	if err := h.accountPool.UpdateLeaderStatus(c.Request.Context(), id, body.Status); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) DeleteLeader(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := h.accountPool.DeleteLeader(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) BatchImportLeaders(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	accounts, parseErrors := parseAccountLines(body.Text)
	var poolLeaders []accountpool.Leader
	for _, a := range accounts {
		poolLeaders = append(poolLeaders, accountpool.Leader{
			Email:         a.Email,
			Password:      a.Password,
			RecoveryEmail: a.RecoveryEmail,
			TOTPSecret:    a.TOTPSecret,
			Status:        "available",
		})
	}
	created, dbErrors, err := h.accountPool.BatchCreateLeaders(c.Request.Context(), poolLeaders)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	allErrors := append(parseErrors, dbErrors...)
	c.JSON(http.StatusOK, gin.H{"created": created, "errors": allErrors, "total_lines": len(accounts) + len(parseErrors)})
}

// --- Proxies ---

func (h *Handler) ListPoolProxies(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	limit, offset := parsePagination(c)
	proxies, total, err := h.accountPool.ListProxies(c.Request.Context(),
		c.Query("type"), c.Query("status"), limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": proxies, "total": total, "limit": limit, "offset": offset})
}

func (h *Handler) CreatePoolProxy(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	var p accountpool.Proxy
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if p.ProxyURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "proxy_url is required"})
		return
	}
	if p.Type == "" {
		p.Type = "member"
	}
	if err := h.accountPool.CreateProxy(c.Request.Context(), &p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, p)
}

func (h *Handler) UpdatePoolProxy(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var p accountpool.Proxy
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	p.ID = id
	if err := h.accountPool.UpdateProxy(c.Request.Context(), &p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, p)
}

func (h *Handler) DeletePoolProxy(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := h.accountPool.DeleteProxy(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) BatchImportProxies(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	var body struct {
		Text string `json:"text"`
		Type string `json:"type"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defaultType := body.Type
	if defaultType == "" {
		defaultType = "member"
	}

	var proxies []accountpool.Proxy
	var parseErrors []string
	lines := strings.Split(strings.TrimSpace(body.Text), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "----", 2)
		url := strings.TrimSpace(parts[0])
		ptype := defaultType
		if len(parts) == 2 {
			t := strings.TrimSpace(parts[1])
			if t == "leader" || t == "member" {
				ptype = t
			} else {
				parseErrors = append(parseErrors, fmt.Sprintf("line %d: invalid type %q", i+1, t))
				continue
			}
		}
		if url == "" {
			parseErrors = append(parseErrors, fmt.Sprintf("line %d: empty proxy URL", i+1))
			continue
		}
		proxies = append(proxies, accountpool.Proxy{ProxyURL: url, Type: ptype, Status: "available"})
	}

	created, dbErrors, err := h.accountPool.BatchCreateProxies(c.Request.Context(), proxies)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	allErrors := append(parseErrors, dbErrors...)
	c.JSON(http.StatusOK, gin.H{"created": created, "errors": allErrors, "total_lines": len(proxies) + len(parseErrors)})
}

func (h *Handler) PickNextAvailableProxy(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	var body struct {
		Type string `json:"type"`
	}
	// Body is optional
	_ = c.ShouldBindJSON(&body)
	p, err := h.accountPool.PickNextAvailableProxy(c.Request.Context(), body.Type)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if p == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no available proxy"})
		return
	}
	c.JSON(http.StatusOK, p)
}

// --- Groups ---

func (h *Handler) ListGroups(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	limit, offset := parsePagination(c)
	groups, total, err := h.accountPool.ListGroups(c.Request.Context(),
		c.Query("group_id"), c.Query("leader_email"), limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": groups, "total": total, "limit": limit, "offset": offset})
}

func (h *Handler) CreateGroup(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	var g accountpool.Group
	if err := c.ShouldBindJSON(&g); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if g.GroupID == "" || g.LeaderEmail == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group_id and leader_email are required"})
		return
	}
	if err := h.accountPool.CreateGroup(c.Request.Context(), &g); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, g)
}

func (h *Handler) UpdateGroup(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var g accountpool.Group
	if err := c.ShouldBindJSON(&g); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	g.ID = id
	if err := h.accountPool.UpdateGroup(c.Request.Context(), &g); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, g)
}

func (h *Handler) DeleteGroup(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := h.accountPool.DeleteGroup(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// --- Batch import parser ---

type parsedAccount struct {
	Email         string
	Password      string
	RecoveryEmail string
	TOTPSecret    string
}

// parseAccountLines parses batch import text.
// Supports formats (separator: "----" or "|"):
//   - email----password----recovery_email----totp_secret (4 fields)
//   - email----password----totp_secret (3 fields)
func parseAccountLines(text string) ([]parsedAccount, []string) {
	var accounts []parsedAccount
	var errors []string
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Auto-detect separator: use "----" if present, otherwise "|"
		sep := "----"
		if !strings.Contains(line, "----") && strings.Contains(line, "|") {
			sep = "|"
		}
		parts := strings.Split(line, sep)
		var a parsedAccount
		switch len(parts) {
		case 4:
			a.Email = strings.TrimSpace(parts[0])
			a.Password = strings.TrimSpace(parts[1])
			a.RecoveryEmail = strings.TrimSpace(parts[2])
			a.TOTPSecret = strings.TrimSpace(parts[3])
		case 3:
			a.Email = strings.TrimSpace(parts[0])
			a.Password = strings.TrimSpace(parts[1])
			a.TOTPSecret = strings.TrimSpace(parts[2])
		default:
			errors = append(errors, fmt.Sprintf("line %d: expected 3 or 4 fields separated by ---- or |", i+1))
			continue
		}
		if a.Email == "" || a.Password == "" {
			errors = append(errors, fmt.Sprintf("line %d: email and password are required", i+1))
			continue
		}
		if a.TOTPSecret == "" {
			errors = append(errors, fmt.Sprintf("line %d: totp_secret is required", i+1))
			continue
		}
		accounts = append(accounts, a)
	}
	return accounts, errors
}
