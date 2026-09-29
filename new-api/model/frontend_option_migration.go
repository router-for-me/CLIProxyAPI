package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/console_setting"
	"gorm.io/gorm"
)

const retiredThemeOptionKey = "theme.frontend"

type legacyOptionTransform func(string) (string, error)

// MigrateRetiredFrontendOptions normalizes options that belonged to the
// removed dashboard frontend. Each legacy console setting is migrated in its
// own transaction so one malformed value cannot block the other settings.
func MigrateRetiredFrontendOptions() error {
	if DB == nil {
		return errors.New("database is not initialized")
	}

	var migrationErrors []error
	if err := normalizeRetiredThemeOption(); err != nil {
		migrationErrors = append(migrationErrors, fmt.Errorf("normalize %s: %w", retiredThemeOptionKey, err))
	}

	migrations := []struct {
		source    string
		target    string
		transform legacyOptionTransform
	}{
		{source: "ApiInfo", target: "console_setting.api_info", transform: transformLegacyAPIInfo},
		{source: "Announcements", target: "console_setting.announcements", transform: transformLegacyAnnouncements},
		{source: "FAQ", target: "console_setting.faq", transform: transformLegacyFAQ},
	}
	for _, migration := range migrations {
		if err := migrateLegacyOption(migration.source, migration.target, migration.transform); err != nil {
			migrationErrors = append(migrationErrors, err)
		}
	}
	if err := migrateLegacyUptimeOptions(); err != nil {
		migrationErrors = append(migrationErrors, err)
	}
	return errors.Join(migrationErrors...)
}

func normalizeRetiredThemeOption() error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var option Option
		err := tx.Where(&Option{Key: retiredThemeOptionKey}).First(&option).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&Option{Key: retiredThemeOptionKey, Value: "default"}).Error
		}
		if err != nil {
			return err
		}
		if option.Value == "default" {
			return nil
		}
		return tx.Model(&option).Update("value", "default").Error
	})
}

func migrateLegacyOption(sourceKey, targetKey string, transform legacyOptionTransform) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var source Option
		if err := tx.Where(&Option{Key: sourceKey}).First(&source).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return fmt.Errorf("read legacy option %s: %w", sourceKey, err)
		}

		var target Option
		err := tx.Where(&Option{Key: targetKey}).First(&target).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("read target option %s: %w", targetKey, err)
		}
		if err == nil {
			return tx.Delete(&source).Error
		}

		value, transformErr := transform(source.Value)
		if transformErr != nil {
			common.SysError(fmt.Sprintf("legacy option %s was not migrated: %v", sourceKey, transformErr))
			return nil
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			target = Option{Key: targetKey}
		}
		target.Value = value
		if err := tx.Save(&target).Error; err != nil {
			return fmt.Errorf("write target option %s: %w", targetKey, err)
		}
		if err := tx.Delete(&source).Error; err != nil {
			return fmt.Errorf("delete legacy option %s: %w", sourceKey, err)
		}
		return nil
	})
}

func transformLegacyAPIInfo(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("value is empty")
	}
	var items []map[string]any
	if err := common.UnmarshalJsonStr(value, &items); err != nil {
		return "", err
	}
	if len(items) > 50 {
		items = items[:50]
	}
	encoded, err := common.Marshal(items)
	if err != nil {
		return "", err
	}
	result := string(encoded)
	if err := console_setting.ValidateConsoleSettings(result, "ApiInfo"); err != nil {
		return "", err
	}
	return result, nil
}

func transformLegacyAnnouncements(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("value is empty")
	}
	if err := console_setting.ValidateConsoleSettings(value, "Announcements"); err != nil {
		return "", err
	}
	return value, nil
}

func transformLegacyFAQ(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("value is empty")
	}
	var legacyItems []map[string]any
	if err := common.UnmarshalJsonStr(value, &legacyItems); err != nil {
		return "", err
	}
	items := make([]map[string]any, 0, len(legacyItems))
	for index, item := range legacyItems {
		question, _ := item["question"].(string)
		if strings.TrimSpace(question) == "" {
			question, _ = item["title"].(string)
		}
		answer, _ := item["answer"].(string)
		if strings.TrimSpace(answer) == "" {
			answer, _ = item["content"].(string)
		}
		if strings.TrimSpace(question) == "" || strings.TrimSpace(answer) == "" {
			return "", fmt.Errorf("FAQ entry %d is missing a question or answer", index)
		}
		items = append(items, map[string]any{"question": question, "answer": answer})
	}
	if len(items) > 50 {
		items = items[:50]
	}
	encoded, err := common.Marshal(items)
	if err != nil {
		return "", err
	}
	result := string(encoded)
	if err := console_setting.ValidateConsoleSettings(result, "FAQ"); err != nil {
		return "", err
	}
	return result, nil
}

func migrateLegacyUptimeOptions() error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var urlOption Option
		urlErr := tx.Where(&Option{Key: "UptimeKumaUrl"}).First(&urlOption).Error
		if urlErr != nil && !errors.Is(urlErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("read legacy option UptimeKumaUrl: %w", urlErr)
		}
		var slugOption Option
		slugErr := tx.Where(&Option{Key: "UptimeKumaSlug"}).First(&slugOption).Error
		if slugErr != nil && !errors.Is(slugErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("read legacy option UptimeKumaSlug: %w", slugErr)
		}
		if errors.Is(urlErr, gorm.ErrRecordNotFound) && errors.Is(slugErr, gorm.ErrRecordNotFound) {
			return nil
		}

		var target Option
		targetErr := tx.Where(&Option{Key: "console_setting.uptime_kuma_groups"}).First(&target).Error
		if targetErr != nil && !errors.Is(targetErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("read target option console_setting.uptime_kuma_groups: %w", targetErr)
		}
		if targetErr == nil {
			if urlErr == nil {
				if err := tx.Delete(&urlOption).Error; err != nil {
					return err
				}
			}
			if slugErr == nil {
				return tx.Delete(&slugOption).Error
			}
			return nil
		}

		if urlErr != nil || slugErr != nil || strings.TrimSpace(urlOption.Value) == "" || strings.TrimSpace(slugOption.Value) == "" {
			common.SysError("legacy Uptime Kuma options were not migrated: both URL and slug are required")
			return nil
		}
		groups := []map[string]any{{
			"id":           1,
			"categoryName": "old",
			"url":          urlOption.Value,
			"slug":         slugOption.Value,
			"description":  "",
		}}
		encoded, err := common.Marshal(groups)
		if err != nil {
			return err
		}
		value := string(encoded)
		if err := console_setting.ValidateConsoleSettings(value, "UptimeKumaGroups"); err != nil {
			common.SysError(fmt.Sprintf("legacy Uptime Kuma options were not migrated: %v", err))
			return nil
		}
		if errors.Is(targetErr, gorm.ErrRecordNotFound) {
			target = Option{Key: "console_setting.uptime_kuma_groups"}
		}
		target.Value = value
		if err := tx.Save(&target).Error; err != nil {
			return fmt.Errorf("write target option console_setting.uptime_kuma_groups: %w", err)
		}
		if err := tx.Delete(&urlOption).Error; err != nil {
			return err
		}
		return tx.Delete(&slugOption).Error
	})
}
