package management

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Alert represents a system alert
type Alert struct {
	ID          string     `json:"id"`
	Type        AlertType  `json:"type"` // error_rate, latency, cost, uptime, provider
	Severity    Severity   `json:"severity"` // critical, warning, info
	Status      AlertStatus `json:"status"` // firing, resolved
	Title       string     `json:"title"`
	Description string     `json:"description"`
	MetricName  string     `json:"metric_name,omitempty"`
	Threshold   float64    `json:"threshold,omitempty"`
	CurrentValue float64  `json:"current_value,omitempty"`
	Provider    string     `json:"provider,omitempty"`
	ModelID     string     `json:"model_id,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// AlertType represents the type of alert
type AlertType string

const (
	AlertTypeErrorRate  AlertType = "error_rate"
	AlertTypeLatency   AlertType = "latency"
	AlertTypeCost      AlertType = "cost"
	AlertTypeUptime    AlertType = "uptime"
	AlertTypeProvider  AlertType = "provider"
	AlertTypeQuota     AlertType = "quota"
	AlertTypeInfo     AlertType = "info"
)

// Severity represents alert severity
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// AlertStatus represents alert status
type AlertStatus string

const (
	AlertStatusFiring  AlertStatus = "firing"
	AlertStatusResolved AlertStatus = "resolved"
)

// AlertRule defines conditions for triggering alerts
type AlertRule struct {
	Name        string      `json:"name"`
	Type        AlertType   `json:"type"`
	Severity    Severity    `json:"severity"`
	Threshold   float64     `json:"threshold"`
	Duration    time.Duration `json:"duration"` // How long condition must be true
	Cooldown    time.Duration `json:"cooldown"` // Time before next alert
	Enabled     bool        `json:"enabled"`
	Notify      []string    `json:"notify"` // notification channels
}

// AlertManager manages alerts and rules
type AlertManager struct {
	mu           sync.RWMutex
	rules        map[string]*AlertRule
	activeAlerts map[string]*Alert
	alertHistory  []Alert
	maxHistory    int
	notifiers    []AlertNotifier
}

// AlertNotifier defines an interface for alert notifications
type AlertNotifier interface {
	Send(ctx context.Context, alert *Alert) error
}

// NewAlertManager creates a new AlertManager
func NewAlertManager() *AlertManager {
	return &AlertManager{
		rules:        make(map[string]*AlertRule),
		activeAlerts: make(map[string]*Alert),
		alertHistory:  make([]Alert, 0),
		maxHistory:    1000,
		notifiers:    make([]AlertNotifier, 0),
	}
}

// AddRule adds an alert rule
func (m *AlertManager) AddRule(rule *AlertRule) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules[rule.Name] = rule
}

// RemoveRule removes an alert rule
func (m *AlertManager) RemoveRule(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rules, name)
}

// GetRules returns all alert rules
func (m *AlertManager) GetRules() []*AlertRule {
	m.mu.RLock()
	defer m.mu.RUnlock()

	rules := make([]*AlertRule, 0, len(m.rules))
	for _, r := range m.rules {
		rules = append(rules, r)
	}
	return rules
}

// AddNotifier adds a notification channel
func (m *AlertManager) AddNotifier(notifier AlertNotifier) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notifiers = append(m.notifiers, notifier)
}

// EvaluateMetrics evaluates current metrics against rules
func (m *AlertManager) EvaluateMetrics(ctx context.Context, metrics map[string]float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, rule := range m.rules {
		if !rule.Enabled {
			continue
		}

		value, exists := metrics[string(rule.Type)]
		if !exists {
			continue
		}

		alertKey := fmt.Sprintf("%s:%s", rule.Name, rule.Type)
		
		switch rule.Type {
		case AlertTypeErrorRate, AlertTypeLatency:
			if value > rule.Threshold {
				m.triggerOrUpdateAlert(ctx, alertKey, rule, value)
			} else {
				m.resolveAlert(ctx, alertKey)
			}
		case AlertTypeCost:
			if value > rule.Threshold {
				m.triggerOrUpdateAlert(ctx, alertKey, rule, value)
			}
		}
	}
}

// triggerOrUpdateAlert triggers or updates an alert
func (m *AlertManager) triggerOrUpdateAlert(ctx context.Context, key string, rule *AlertRule, value float64) {
	if existing, ok := m.activeAlerts[key]; ok {
		// Update existing alert
		existing.CurrentValue = value
		return
	}

	// Create new alert
	alert := &Alert{
		ID:           fmt.Sprintf("alert-%d", time.Now().Unix()),
		Type:         rule.Type,
		Severity:     rule.Severity,
		Status:       AlertStatusFiring,
		Title:        fmt.Sprintf("%s %s", rule.Type, getSeverityText(rule.Severity)),
		Description:  fmt.Sprintf("%s exceeded threshold: %.2f > %.2f", rule.Type, value, rule.Threshold),
		MetricName:   string(rule.Type),
		Threshold:    rule.Threshold,
		CurrentValue: value,
		StartedAt:    time.Now(),
		CreatedAt:    time.Now(),
	}

	m.activeAlerts[key] = alert
	m.alertHistory = append(m.alertHistory, *alert)

	// Send notifications
	m.sendNotifications(ctx, alert)
}

// resolveAlert resolves an active alert
func (m *AlertManager) resolveAlert(ctx context.Context, key string) {
	alert, ok := m.activeAlerts[key]
	if !ok {
		return
	}

	now := time.Now()
	alert.Status = AlertStatusResolved
	alert.ResolvedAt = &now

	delete(m.activeAlerts, key)
	m.alertHistory = append(m.alertHistory, *alert)
}

// sendNotifications sends alert to all notifiers
func (m *AlertManager) sendNotifications(ctx context.Context, alert *Alert) {
	for _, notifier := range m.notifiers {
		if err := notifier.Send(ctx, alert); err != nil {
			// Log error but continue
			fmt.Printf("Failed to send notification: %v\n", err)
		}
	}
}

// GetActiveAlerts returns all active alerts
func (m *AlertManager) GetActiveAlerts() []Alert {
	m.mu.RLock()
	defer m.mu.RUnlock()

	alerts := make([]Alert, 0, len(m.activeAlerts))
	for _, a := range m.activeAlerts {
		alerts = append(alerts, *a)
	}
	return alerts
}

// GetAlertHistory returns alert history
func (m *AlertManager) GetAlertHistory(limit int) []Alert {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if limit <= 0 || limit > len(m.alertHistory) {
		limit = len(m.alertHistory)
	}
	
	result := make([]Alert, limit)
	copy(result, m.alertHistory[len(m.alertHistory)-limit:])
	return result
}

// getSeverityText returns text description of severity
func getSeverityText(s Severity) string {
	switch s {
	case SeverityCritical:
		return "critical alert"
	case SeverityWarning:
		return "warning"
	case SeverityInfo:
		return "info"
	default:
		return "alert"
	}
}

// CommonAlertRules returns typical alert rules
func CommonAlertRules() []*AlertRule {
	return []*AlertRule{
		{
			Name:     "high-error-rate",
			Type:     AlertTypeErrorRate,
			Severity: SeverityCritical,
			Threshold: 5.0, // 5% error rate
			Duration: 5 * time.Minute,
			Cooldown: 15 * time.Minute,
			Enabled:  true,
			Notify:   []string{"slack", "email"},
		},
		{
			Name:     "high-latency",
			Type:     AlertTypeLatency,
			Severity: SeverityWarning,
			Threshold: 10000, // 10 seconds
			Duration: 10 * time.Minute,
			Cooldown: 30 * time.Minute,
			Enabled:  true,
			Notify:   []string{"slack"},
		},
		{
			Name:     "high-cost",
			Type:     AlertTypeCost,
			Severity: SeverityWarning,
			Threshold: 1000.0, // $1000
			Duration: 1 * time.Hour,
			Cooldown: 1 * time.Hour,
			Enabled:  true,
			Notify:   []string{"email"},
		},
		{
			Name:     "provider-outage",
			Type:     AlertTypeProvider,
			Severity: SeverityCritical,
			Threshold: 90.0, // 90% uptime threshold
			Duration: 5 * time.Minute,
			Cooldown: 10 * time.Minute,
			Enabled:  true,
			Notify:   []string{"slack", "email", "pagerduty"},
		},
	}
}

// AlertHandler handles alert API endpoints
type AlertHandler struct {
	manager *AlertManager
}

// NewAlertHandler creates a new AlertHandler
func NewAlertHandler() *AlertHandler {
	m := NewAlertManager()
	// Add default rules
	for _, rule := range CommonAlertRules() {
		m.AddRule(rule)
	}
	return &AlertHandler{manager: m}
}

// GETAlerts handles GET /v1/alerts
func (h *AlertHandler) GETAlerts(c *gin.Context) {
	status := c.Query("status")
	alertType := c.Query("type")

	alerts := h.manager.GetActiveAlerts()

	// Filter
	if status != "" {
		var filtered []Alert
		for _, a := range alerts {
			if string(a.Status) == status {
				filtered = append(filtered, a)
			}
		}
		alerts = filtered
	}

	if alertType != "" {
		var filtered []Alert
		for _, a := range alerts {
			if string(a.Type) == alertType {
				filtered = append(filtered, a)
			}
		}
		alerts = filtered
	}

	c.JSON(http.StatusOK, gin.H{
		"count":  len(alerts),
		"alerts": alerts,
	})
}

// GETAlertHistory handles GET /v1/alerts/history
func (h *AlertHandler) GETAlertHistory(c *gin.Context) {
	limit := 50
	fmt.Sscanf(c.DefaultQuery("limit", "50"), "%d", &limit)

	history := h.manager.GetAlertHistory(limit)

	c.JSON(http.StatusOK, gin.H{
		"count":  len(history),
		"alerts": history,
	})
}

// GETAlertRules handles GET /v1/alerts/rules
func (h *AlertHandler) GETAlertRules(c *gin.Context) {
	rules := h.manager.GetRules()
	c.JSON(http.StatusOK, gin.H{"rules": rules})
}

// POSTAlertRule handles POST /v1/alerts/rules
func (h *AlertHandler) POSTAlertRule(c *gin.Context) {
	var rule AlertRule
	if err := c.ShouldBindJSON(&rule); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.manager.AddRule(&rule)
	c.JSON(http.StatusCreated, gin.H{"message": "rule created", "rule": rule})
}

// DELETEAlertRule handles DELETE /v1/alerts/rules/:name
func (h *AlertHandler) DELETEAlertRule(c *gin.Context) {
	name := c.Param("name")
	h.manager.RemoveRule(name)
	c.JSON(http.StatusOK, gin.H{"message": "rule deleted"})
}

// POSTTestAlert handles POST /v1/alerts/test (for testing notifications)
func (h *AlertHandler) POSTTestAlert(c *gin.Context) {
	alert := &Alert{
		ID:          "test-alert",
		Type:        AlertTypeInfo,
		Severity:    SeverityInfo,
		Status:      AlertStatusFiring,
		Title:       "Test Alert",
		Description: "This is a test alert",
		StartedAt:   time.Now(),
		CreatedAt:   time.Now(),
	}

	ctx := c.Request.Context()
	h.manager.sendNotifications(ctx, alert)

	c.JSON(http.StatusOK, gin.H{"message": "test alert sent"})
}
