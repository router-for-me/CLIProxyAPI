package management

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/store/accountpool"
)

// --- Group Runs ---

func (h *Handler) ListGroupRuns(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	limit, offset := parsePagination(c)
	date := c.Query("date")
	groupID := -1 // -1 means no filter
	if gid := c.Query("group_id"); gid != "" {
		if v, err := strconv.Atoi(gid); err == nil {
			groupID = v
		}
	}
	status := c.Query("status")
	runs, total, err := h.accountPool.ListGroupRuns(c.Request.Context(), date, groupID, status, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": runs, "total": total, "limit": limit, "offset": offset})
}

func (h *Handler) GetGroupRun(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	rw, err := h.accountPool.GetGroupRunWithMembers(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if rw == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "group_run not found"})
		return
	}
	c.JSON(http.StatusOK, rw)
}

func (h *Handler) CreateGroupRun(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	var r accountpool.GroupRun
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if r.LeaderID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "leader_id is required"})
		return
	}
	if err := h.accountPool.CreateGroupRun(c.Request.Context(), &r); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, r)
}

func (h *Handler) UpdateGroupRun(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var r accountpool.GroupRun
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	r.ID = id
	if err := h.accountPool.UpdateGroupRun(c.Request.Context(), &r); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, r)
}

func (h *Handler) DeleteGroupRun(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := h.accountPool.DeleteGroupRun(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) GetGroupRunJSON(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	data, err := h.accountPool.GetGroupRunJSON(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if data == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "group_run not found"})
		return
	}
	c.JSON(http.StatusOK, data)
}

// --- Group Run Members ---

func (h *Handler) AddGroupRunMembers(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var body struct {
		Members []accountpool.GroupMember `json:"members"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(body.Members) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "members array is required"})
		return
	}
	created, err := h.accountPool.AddGroupMembers(c.Request.Context(), id, body.Members)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"created": created})
}

func parseMemberID(c *gin.Context) (int64, bool) {
	mid, err := strconv.ParseInt(c.Param("mid"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid member id"})
		return 0, false
	}
	return mid, true
}

func (h *Handler) UpdateGroupRunMember(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	runID, ok := parseID(c)
	if !ok {
		return
	}
	memberID, ok := parseMemberID(c)
	if !ok {
		return
	}
	var fields map[string]interface{}
	if err := c.ShouldBindJSON(&fields); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.accountPool.UpdateGroupMember(c.Request.Context(), runID, memberID, fields); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) UpdateGroupRunMemberStatus(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	runID, ok := parseID(c)
	if !ok {
		return
	}
	memberID, ok := parseMemberID(c)
	if !ok {
		return
	}
	var body struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Status == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status is required"})
		return
	}
	if err := h.accountPool.UpdateGroupMemberStatus(c.Request.Context(), runID, memberID, body.Status, body.Message); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) ReplaceGroupRunMember(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	runID, ok := parseID(c)
	if !ok {
		return
	}
	memberID, ok := parseMemberID(c)
	if !ok {
		return
	}
	var body struct {
		Reason     string `json:"reason"`
		ReuseProxy *bool  `json:"reuse_proxy"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.Reason == "" {
		body.Reason = "failed"
	}
	reuseProxy := true
	if body.ReuseProxy != nil {
		reuseProxy = *body.ReuseProxy
	}
	result, err := h.accountPool.ReplaceMember(c.Request.Context(), runID, memberID, body.Reason, reuseProxy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) DeleteGroupRunMembers(c *gin.Context) {
	if !h.requireAccountPool(c) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := h.accountPool.DeleteGroupMembers(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
