package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	AuditCategoryLogin       = "login"
	AuditCategorySecurity    = "security"
	AuditCategoryOperation   = "operation"
	AuditCategoryAccessToken = "access_token"
)

// AuditLog is retained independently of usage logs and their cleanup/TTL policy.
// TokenRef identifies a PAT generation without storing its bearer credential.
type AuditLog struct {
	Id         int        `json:"id"`
	EventId    string     `json:"event_id" gorm:"type:varchar(64);uniqueIndex"`
	UserId     int        `json:"user_id" gorm:"index:idx_audit_user_time,priority:1"`
	Username   string     `json:"username" gorm:"type:varchar(64);index"`
	ActorRole  int        `json:"actor_role"` // Immutable role of the actor when the event began, not the log owner.
	CreatedAt  int64      `json:"created_at" gorm:"type:bigint;index:idx_audit_user_time,priority:2;index:idx_audit_token_time,priority:2;index"`
	Category   string     `json:"category" gorm:"type:varchar(24);index"`
	Action     string     `json:"action" gorm:"type:varchar(128)"`
	TokenRef   string     `json:"token_ref" gorm:"type:varchar(64);index:idx_audit_token_time,priority:1"`
	AuthMethod string     `json:"auth_method" gorm:"type:varchar(24)"`
	Ip         string     `json:"ip" gorm:"type:varchar(64)"`
	UserAgent  string     `json:"user_agent" gorm:"type:varchar(512)"`
	Method     string     `json:"method" gorm:"type:varchar(16)"`
	Route      string     `json:"route" gorm:"type:varchar(255)"`
	Status     int        `json:"status"`
	Success    bool       `json:"success"`
	RequestId  string     `json:"request_id" gorm:"type:varchar(64);index"`
	Content    string     `json:"content" gorm:"type:text"`
	Other      AuditOther `json:"other" gorm:"type:json"`
}

type AuditLogFilter struct {
	SelfView        bool // Server-selected metadata projection, independent of the viewer's actual role.
	UserId          int
	Username        string
	Category        string
	TokenRef        string
	ExcludeTokenRef string
	RequestId       string
	StartTimestamp  int64
	EndTimestamp    int64
	Success         *bool
}

func AccessTokenFingerprint(token string) string {
	// PostgreSQL returns CHAR(32) tokens padded with spaces. Normalize that
	// storage padding so persisted tokens and incoming credentials share a ref.
	token = strings.TrimRight(token, " ")
	if token == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", digest)
}

// RecordAuditLog captures safe request metadata only; raw URLs, query strings,
// credentials and response/request bodies must never enter this table.
func RecordAuditLog(c *gin.Context, entry AuditLog) {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
		entry.RequestId = c.GetString(common.RequestIdKey)
		entry.Ip = c.ClientIP()
		entry.UserAgent = c.Request.UserAgent()
		entry.Method = c.Request.Method
		entry.Route = c.FullPath()
		if entry.Status == 0 {
			entry.Status = c.Writer.Status()
		}
		if entry.AuthMethod == "" {
			entry.AuthMethod = "session"
			if c.GetBool("use_access_token") {
				entry.AuthMethod = "access_token"
			}
		}
	}
	if entry.CreatedAt == 0 {
		entry.CreatedAt = common.GetTimestamp()
	}
	if entry.RequestId == "" {
		entry.RequestId = common.NewRequestId()
	}
	if entry.EventId == "" {
		entry.EventId = common.NewRequestId()
	}
	switch entry.ActorRole {
	case common.RoleCommonUser, common.RoleAdminUser, common.RoleRootUser:
	default:
		logger.LogError(ctx, fmt.Sprintf("audit actor role unavailable (request_id=%s, actor_role=%d)", entry.RequestId, entry.ActorRole))
		entry.ActorRole = 0 // Unknown actors remain visible to root only.
	}
	if entry.Username == "" {
		entry.Username, _ = GetUsernameById(entry.UserId, false)
	}
	ua := []rune(entry.UserAgent)
	if len(ua) > 512 {
		entry.UserAgent = string(ua[:512])
	}
	if LOG_DB == nil {
		logger.LogError(ctx, fmt.Sprintf("audit log write failed (request_id=%s): log database unavailable", entry.RequestId))
		return
	}
	var row any = &entry
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		encoded, err := common.Marshal(entry.Other)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("audit log write failed (request_id=%s): %v", entry.RequestId, err))
			return
		}
		// The ClickHouse GORM insert callback passes structs to the native
		// driver without resolving their Valuer. Bind this column's JSON
		// encoding while retaining AuditOther in the domain and API models.
		row = &struct {
			AuditLog     `gorm:"embedded"`
			EncodedOther string `gorm:"column:other;type:json"`
		}{AuditLog: entry, EncodedOther: string(encoded)}
	}
	if err := LOG_DB.Table("audit_logs").Create(row).Error; err != nil {
		logger.LogError(ctx, fmt.Sprintf("audit log write failed (request_id=%s): %v", entry.RequestId, err))
	}
}

func GetAuditLogs(filter AuditLogFilter, start, limit, viewerRole int) ([]*AuditLog, int64, error) {
	query := LOG_DB.Model(&AuditLog{})
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		// Decode native JSON through database/sql as text for AuditOther.Scan.
		// Preserve numeric metadata instead of returning quoted Int64 values.
		query = query.WithContext(clickhouse.Context(query.Statement.Context, clickhouse.WithSettings(clickhouse.Settings{
			"output_format_native_write_json_as_string": 1,
			"output_format_json_quote_64bit_integers":   0,
		})))
	}
	if viewerRole < common.RoleRootUser {
		query = query.Where("actor_role IN ?", []int{common.RoleCommonUser, common.RoleAdminUser})
	}
	if filter.UserId > 0 {
		query = query.Where("user_id = ?", filter.UserId)
	}
	if filter.Username != "" {
		query = query.Where("username = ?", filter.Username)
	}
	if filter.Category != "" {
		query = query.Where("category = ?", filter.Category)
	}
	if filter.TokenRef != "" {
		query = query.Where("token_ref = ?", filter.TokenRef)
	}
	if filter.ExcludeTokenRef != "" {
		query = query.Where("token_ref <> ?", filter.ExcludeTokenRef)
	}
	if filter.RequestId != "" {
		query = query.Where("request_id = ?", filter.RequestId)
	}
	if filter.StartTimestamp > 0 {
		query = query.Where("created_at >= ?", filter.StartTimestamp)
	}
	if filter.EndTimestamp > 0 {
		query = query.Where("created_at <= ?", filter.EndTimestamp)
	}
	if filter.Success != nil {
		query = query.Where("success = ?", *filter.Success)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	logs := make([]*AuditLog, 0)
	if err := query.Order("created_at DESC").Order("event_id DESC").Offset(start).Limit(limit).Find(&logs).Error; err != nil {
		return nil, 0, err
	}
	visibility := logOtherVisibilityUser
	if !filter.SelfView && viewerRole >= common.RoleRootUser {
		visibility = logOtherVisibilityRoot
	} else if !filter.SelfView && viewerRole >= common.RoleAdminUser {
		visibility = logOtherVisibilityAdmin
	}
	for _, entry := range logs {
		if visibility != logOtherVisibilityRoot {
			entry.Other.RootInfo = nil
		}
		if visibility == logOtherVisibilityUser {
			entry.Other.AdminInfo = nil
			entry.Other.AuditInfo = nil
		}
	}
	return logs, total, nil
}

type UserAccessTokenStatus struct {
	Exists     bool   `json:"exists"`
	TokenRef   string `json:"token_ref"`
	CreatedAt  *int64 `json:"created_at"`
	LastUsedAt *int64 `json:"last_used_at"`
	LastUsedIp string `json:"last_used_ip"`
}

func GetUserAccessTokenStatus(userId int) (*UserAccessTokenStatus, error) {
	var user User
	if err := DB.Select("id", "role", "access_token", "access_token_created_at").First(&user, userId).Error; err != nil {
		return nil, err
	}
	status := &UserAccessTokenStatus{Exists: user.GetAccessToken() != ""}
	if !status.Exists {
		return status, nil
	}
	status.TokenRef = AccessTokenFingerprint(user.GetAccessToken())
	status.CreatedAt = user.AccessTokenCreatedAt
	var latest AuditLog
	query := LOG_DB.Select("created_at", "ip").Where("user_id = ? AND token_ref = ? AND category = ?", userId, status.TokenRef, AuditCategoryAccessToken)
	if user.Role < common.RoleRootUser {
		query = query.Where("actor_role IN ?", []int{common.RoleCommonUser, common.RoleAdminUser})
	}
	err := query.Order("created_at DESC").Order("event_id DESC").Take(&latest).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return status, nil
	}
	if err != nil {
		return nil, err
	}
	status.LastUsedAt = &latest.CreatedAt
	status.LastUsedIp = latest.Ip
	return status, nil
}

// MigrateAuditLogs also supports independently configured ClickHouse log stores.
// No TTL clause or usage-log cleanup integration is intentional.
func MigrateAuditLogs() error {
	if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return LOG_DB.AutoMigrate(&AuditLog{})
	}
	return LOG_DB.Exec(`CREATE TABLE IF NOT EXISTS audit_logs (
		id Int64 DEFAULT 0, event_id String, user_id Int64, username String, actor_role Int32,
		created_at Int64, category String, action String, token_ref String,
		auth_method String, ip String, user_agent String, method String, route String,
		status Int32, success UInt8, request_id String, content String, other JSON
	) ENGINE = MergeTree()
	PARTITION BY toYYYYMM(toDateTime(created_at))
	ORDER BY (created_at, event_id)`).Error
}

func ValidAuditCategory(category string) bool {
	return category == "" || category == AuditCategoryLogin || category == AuditCategorySecurity || category == AuditCategoryOperation || category == AuditCategoryAccessToken
}

func ValidTokenFingerprint(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != 64 {
		return false
	}
	return strings.Trim(value, "0123456789abcdef") == ""
}
