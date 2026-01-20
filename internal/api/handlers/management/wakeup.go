package management

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/wakeup"
)

// WakeupHandler manages wakeup-related API endpoints.
type WakeupHandler struct {
	scheduler *wakeup.Scheduler
}

// NewWakeupHandler creates a new wakeup handler.
func NewWakeupHandler(scheduler *wakeup.Scheduler) *WakeupHandler {
	return &WakeupHandler{scheduler: scheduler}
}

// SetScheduler updates the scheduler reference.
func (h *WakeupHandler) SetScheduler(scheduler *wakeup.Scheduler) {
	h.scheduler = scheduler
}

// GetState returns the current wakeup scheduler state.
func (h *WakeupHandler) GetState(c *gin.Context) {
	if h.scheduler == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "wakeup scheduler not initialized"})
		return
	}
	state := h.scheduler.GetState()
	c.JSON(http.StatusOK, state)
}

// GetSchedules returns all configured wakeup schedules.
func (h *WakeupHandler) GetSchedules(c *gin.Context) {
	if h.scheduler == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "wakeup scheduler not initialized"})
		return
	}
	schedules := h.scheduler.GetSchedules()
	c.JSON(http.StatusOK, gin.H{"schedules": schedules})
}

// GetSchedule returns a specific schedule by ID.
func (h *WakeupHandler) GetSchedule(c *gin.Context) {
	if h.scheduler == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "wakeup scheduler not initialized"})
		return
	}
	id := c.Param("id")
	schedule, found := h.scheduler.GetSchedule(id)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "schedule not found"})
		return
	}
	c.JSON(http.StatusOK, schedule)
}

// CreateSchedule creates a new wakeup schedule.
func (h *WakeupHandler) CreateSchedule(c *gin.Context) {
	if h.scheduler == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "wakeup scheduler not initialized"})
		return
	}

	var schedule wakeup.Schedule
	if err := c.ShouldBindJSON(&schedule); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}

	if schedule.Provider == "" {
		schedule.Provider = "antigravity"
	}
	if schedule.Type == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "schedule type is required"})
		return
	}

	if err := h.scheduler.AddSchedule(schedule); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"status": "ok", "id": schedule.ID})
}

// UpdateSchedule updates an existing wakeup schedule.
func (h *WakeupHandler) UpdateSchedule(c *gin.Context) {
	if h.scheduler == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "wakeup scheduler not initialized"})
		return
	}

	id := c.Param("id")
	var schedule wakeup.Schedule
	if err := c.ShouldBindJSON(&schedule); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}

	schedule.ID = id
	if err := h.scheduler.UpdateSchedule(schedule); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// DeleteSchedule deletes a wakeup schedule.
func (h *WakeupHandler) DeleteSchedule(c *gin.Context) {
	if h.scheduler == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "wakeup scheduler not initialized"})
		return
	}

	id := c.Param("id")
	if err := h.scheduler.DeleteSchedule(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Trigger manually triggers a wakeup execution.
func (h *WakeupHandler) Trigger(c *gin.Context) {
	if h.scheduler == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "wakeup scheduler not initialized"})
		return
	}

	var req wakeup.TriggerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Allow empty body - use defaults
		req = wakeup.TriggerRequest{Provider: "antigravity"}
	}

	if req.Provider == "" {
		req.Provider = "antigravity"
	}

	resp := h.scheduler.Trigger(c.Request.Context(), req)
	c.JSON(http.StatusOK, resp)
}

// GetHistory returns wakeup execution history.
func (h *WakeupHandler) GetHistory(c *gin.Context) {
	if h.scheduler == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "wakeup scheduler not initialized"})
		return
	}

	history := h.scheduler.GetHistory()
	if history == nil {
		c.JSON(http.StatusOK, gin.H{"records": []wakeup.WakeupRecord{}, "total": 0})
		return
	}

	// Parse query parameters
	limit := 100
	offset := 0
	scheduleID := c.Query("schedule_id")
	accountID := c.Query("account_id")

	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if o := c.Query("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	records := history.List(limit, offset, scheduleID, accountID)
	total := history.Count(scheduleID, accountID)
	stats := history.Stats()

	c.JSON(http.StatusOK, gin.H{
		"records": records,
		"total":   total,
		"stats":   stats,
	})
}

// ClearHistory clears all wakeup history records.
func (h *WakeupHandler) ClearHistory(c *gin.Context) {
	if h.scheduler == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "wakeup scheduler not initialized"})
		return
	}

	history := h.scheduler.GetHistory()
	if history != nil {
		history.Clear()
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

