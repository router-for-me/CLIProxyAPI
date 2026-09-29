package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

type TaskPluginChannelRef struct {
	Id   int    `json:"id"`
	Name string `json:"name"`
	Type int    `json:"type"`
}

func GetTaskPluginUsage(key string) ([]TaskPluginChannelRef, int64, error) {
	var channels []Channel
	boundTypes := []int{constant.ChannelTypeTaskPlugin, constant.ChannelTypeNewAPI}
	if err := DB.Where("type IN ? AND status = ?", boundTypes, common.ChannelStatusEnabled).Find(&channels).Error; err != nil {
		return nil, 0, err
	}
	refs := make([]TaskPluginChannelRef, 0)
	for _, channel := range channels {
		if channel.GetSetting().BindsTaskPlugin(key) {
			refs = append(refs, TaskPluginChannelRef{Id: channel.Id, Name: channel.Name, Type: channel.Type})
		}
	}
	var inFlight int64
	err := DB.Model(&Task{}).Where("platform = ? AND status NOT IN ?", key, []TaskStatus{TaskStatusSuccess, TaskStatusFailure}).Count(&inFlight).Error
	return refs, inFlight, err
}

// UnbindTaskPlugin removes one plugin from a channel's bindings and reports
// whether the channel changed. A New API gateway channel keeps serving its other
// plugins and its ordinary traffic.
func UnbindTaskPlugin(channelID int, key string) (bool, error) {
	channel, err := GetChannelById(channelID, false)
	if err != nil {
		return false, err
	}
	setting := channel.GetSetting()
	if !setting.BindsTaskPlugin(key) {
		return false, nil
	}
	if setting.TaskPluginKey == key {
		setting.TaskPluginKey = ""
	}
	setting.TaskExtendPluginKeys = slices.DeleteFunc(setting.TaskExtendPluginKeys, func(bound string) bool { return bound == key })
	channel.SetSetting(setting)
	return true, DB.Model(&Channel{}).Where("id = ?", channelID).Update("setting", channel.Setting).Error
}

// LongText is a string column sized for plugin payloads on every supported
// database: longtext on MySQL, text on PostgreSQL and SQLite. A bare
// `type:text` tag stops at 64 KiB on MySQL. A `size:` tag above that becomes
// varchar(N) on PostgreSQL, which rejects sizes past 10485760, and a NOT NULL
// column declared that way is re-altered by AutoMigrate on every MySQL start
// because the reported column length never equals N. Neither type chosen here
// carries a length, so an up-to-date column is left alone, and MySQL widens a
// shipped text column once, without data loss.
type LongText string

func (LongText) GormDBDataType(db *gorm.DB, _ *schema.Field) string {
	if db.Dialector.Name() == string(common.DatabaseTypeMySQL) {
		return "longtext"
	}
	return "text"
}

type TaskPlugin struct {
	Id         int64    `json:"id"`
	Key        string   `json:"key" gorm:"size:128;not null;uniqueIndex:uk_task_plugin_key_version,priority:1"`
	APIVersion int      `json:"api_version" gorm:"not null"`
	Version    string   `json:"version" gorm:"size:64;not null;uniqueIndex:uk_task_plugin_key_version,priority:2"`
	Source     LongText `json:"source" gorm:"not null"`
	SourceHash string   `json:"source_hash" gorm:"size:64;not null"`
	// Icon is the plugin logo shipped as a sidecar icon.svg / icon.png next to
	// plugin.js, stored as a data URI so one column carries both the media
	// type and the bytes. It never travels inside list or detail JSON; the UI
	// loads it through GET /api/plugin/task/:key/icon. LongText keeps the
	// 512 KiB icon cap storable on MySQL, where a bare TEXT holds only 64 KiB.
	Icon      LongText `json:"-"`
	Enabled   bool     `json:"enabled" gorm:"not null"`
	Active    bool     `json:"active" gorm:"not null;index"`
	CreatedAt int64    `json:"created_at" gorm:"not null"`
	Remark    string   `json:"remark" gorm:"type:text"`
}

// HasIcon reports whether this version ships a logo.
func (plugin TaskPlugin) HasIcon() bool {
	return plugin.Icon != ""
}

func SaveTaskPlugin(plugin *TaskPlugin) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var existing TaskPlugin
		err := tx.Where(&TaskPlugin{Key: plugin.Key, Version: plugin.Version}).First(&existing).Error
		if err == nil {
			if existing.SourceHash != plugin.SourceHash {
				return errors.New("plugin key and version already exist with different source")
			}
			updates := map[string]any{"enabled": plugin.Enabled, "remark": plugin.Remark}
			if plugin.Icon != "" {
				updates["icon"] = plugin.Icon
				existing.Icon = plugin.Icon
			}
			if err = tx.Model(&existing).Updates(updates).Error; err != nil {
				return err
			}
			existing.Enabled = plugin.Enabled
			existing.Remark = plugin.Remark
			*plugin = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		plugin.CreatedAt = time.Now().Unix()
		var count int64
		if err = tx.Model(&TaskPlugin{}).Where(&TaskPlugin{Key: plugin.Key, Active: true}).Count(&count).Error; err != nil {
			return err
		}
		plugin.Active = count == 0
		return tx.Create(plugin).Error
	})
}

func ListTaskPluginVersions(key string) ([]TaskPlugin, error) {
	var plugins []TaskPlugin
	err := DB.Where(&TaskPlugin{Key: key}).Order("created_at DESC, id DESC").Find(&plugins).Error
	return plugins, err
}

func ListTaskPlugins() ([]TaskPlugin, error) {
	var plugins []TaskPlugin
	err := DB.
		Order(clause.OrderByColumn{Column: clause.Column{Name: "key"}}).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "created_at"}, Desc: true}).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "id"}, Desc: true}).
		Find(&plugins).Error
	return plugins, err
}

func GetTaskPluginVersion(key, version string) (*TaskPlugin, error) {
	var plugin TaskPlugin
	query := DB.Where(&TaskPlugin{Key: key})
	if version == "" {
		query = query.Where(&TaskPlugin{Active: true})
	} else {
		query = query.Where(&TaskPlugin{Version: version})
	}
	if err := query.First(&plugin).Error; err != nil {
		return nil, err
	}
	return &plugin, nil
}

// ListActiveTaskPlugins returns the enabled override rows without their
// source and icon payloads; see GetTaskPluginSyncSnapshot.
func ListActiveTaskPlugins() ([]TaskPlugin, error) {
	snapshot, err := GetTaskPluginSyncSnapshot()
	return snapshot.Plugins, err
}

type TaskPluginSyncSnapshot struct {
	// Plugins are the enabled active overrides with Source and Icon left
	// empty. Sync compares SourceHash against what it already compiled and
	// loads source through GetTaskPluginSource only for the rows that changed.
	Plugins  []TaskPlugin
	Revision string
}

// GetTaskPluginSyncSnapshot returns the enabled override set together with a
// deterministic revision of every active database override. Nodes can compare
// the revision even though their local routing-generation counters differ.
// The query skips the source and icon columns: every node polls this every
// 30 seconds, and plugin sources may be several MiB each.
func GetTaskPluginSyncSnapshot() (TaskPluginSyncSnapshot, error) {
	var activePlugins []TaskPlugin
	if err := DB.Omit("source", "icon").Where(&TaskPlugin{Active: true}).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "key"}}).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "version"}}).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "id"}}).
		Find(&activePlugins).Error; err != nil {
		return TaskPluginSyncSnapshot{}, err
	}

	type revisionEntry struct {
		Key        string `json:"key"`
		APIVersion int    `json:"api_version"`
		Version    string `json:"version"`
		SourceHash string `json:"source_hash"`
		Enabled    bool   `json:"enabled"`
	}
	entries := make([]revisionEntry, 0, len(activePlugins))
	enabledPlugins := make([]TaskPlugin, 0, len(activePlugins))
	for _, plugin := range activePlugins {
		entries = append(entries, revisionEntry{
			Key:        plugin.Key,
			APIVersion: plugin.APIVersion,
			Version:    plugin.Version,
			SourceHash: plugin.SourceHash,
			Enabled:    plugin.Enabled,
		})
		if plugin.Enabled {
			enabledPlugins = append(enabledPlugins, plugin)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Key != entries[j].Key {
			return entries[i].Key < entries[j].Key
		}
		return entries[i].Version < entries[j].Version
	})
	payload, err := common.Marshal(entries)
	if err != nil {
		return TaskPluginSyncSnapshot{}, err
	}
	digest := sha256.Sum256(payload)
	return TaskPluginSyncSnapshot{
		Plugins:  enabledPlugins,
		Revision: hex.EncodeToString(digest[:]),
	}, nil
}

// GetTaskPluginSource loads one override's JavaScript by row id. Rows never
// change source in place (SaveTaskPlugin rejects a different source for the
// same key and version), so the id from a sync snapshot identifies exactly the
// text whose SourceHash that snapshot reported.
func GetTaskPluginSource(id int64) (LongText, error) {
	var plugin TaskPlugin
	err := DB.Select("source").Where(&TaskPlugin{Id: id}).Take(&plugin).Error
	return plugin.Source, err
}

func ActivateTaskPlugin(key, version string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var target TaskPlugin
		if err := tx.Where(&TaskPlugin{Key: key, Version: version}).First(&target).Error; err != nil {
			return err
		}
		if err := tx.Model(&TaskPlugin{}).Where(&TaskPlugin{Key: key}).Update("active", false).Error; err != nil {
			return err
		}
		return tx.Model(&target).Updates(map[string]any{"active": true, "enabled": true}).Error
	})
}

func SetTaskPluginEnabled(key string, enabled bool) error {
	result := DB.Model(&TaskPlugin{}).Where(&TaskPlugin{Key: key, Active: true}).Update("enabled", enabled)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

type TaskPluginDeleteResult struct {
	DeletedActive bool
	Promoted      *TaskPlugin
}

func DeleteTaskPluginVersion(key, version string) (TaskPluginDeleteResult, error) {
	result := TaskPluginDeleteResult{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var plugin TaskPlugin
		if err := lockForUpdate(tx).Where(&TaskPlugin{Key: key, Version: version}).First(&plugin).Error; err != nil {
			return err
		}
		result.DeletedActive = plugin.Active
		if err := tx.Delete(&plugin).Error; err != nil {
			return err
		}
		if !plugin.Active {
			return nil
		}

		var promoted TaskPlugin
		err := lockForUpdate(tx).
			Where(&TaskPlugin{Key: key}).
			Order("created_at DESC, id DESC").
			First(&promoted).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = tx.Model(&promoted).Update("active", true).Error; err != nil {
			return err
		}
		promoted.Active = true
		result.Promoted = &promoted
		return nil
	})
	return result, err
}
