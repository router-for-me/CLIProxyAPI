package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var metadataMutationMu sync.Mutex

// metadataTransaction serializes changes to vendors and model ownership before
// acquiring model/vendor row locks. The option anchor also covers empty tables.
func metadataTransaction(change func(*gorm.DB) error) error {
	metadataMutationMu.Lock()
	defer metadataMutationMu.Unlock()
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := lockMetadataMutation(tx); err != nil {
			return err
		}
		return change(tx)
	})
}

func lockMetadataMutation(tx *gorm.DB) error {
	anchor := Option{Key: "metadata_sync_lock", Value: ""}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&anchor).Error; err != nil {
		return err
	}
	return lockForUpdate(tx).Where(commonKeyCol+" = ?", anchor.Key).First(&anchor).Error
}

var ErrMetadataSyncConflict = errors.New("metadata changed; preview again before applying")

var MetadataSyncFields = []string{"description", "icon", "tags", "vendor", "endpoints", "name_rule", "status"}

type MetadataValues struct {
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Tags        string `json:"tags"`
	Vendor      string `json:"vendor"`
	Endpoints   string `json:"endpoints"`
	NameRule    int    `json:"name_rule"`
	Status      int    `json:"status"`
}

type MetadataSyncSelection struct {
	ModelName     string   `json:"model_name"`
	RecordVersion string   `json:"record_version"`
	Create        bool     `json:"create"`
	Fields        []string `json:"fields"`
}

type MetadataSyncUpdate struct {
	MetadataSyncSelection
	Values MetadataValues
}

type MetadataSyncResult struct {
	CreatedModels  []string                `json:"created_models"`
	UpdatedModels  []MetadataSyncSelection `json:"updated_models"`
	CreatedVendors []string                `json:"created_vendors"`
}

func MetadataRecordVersion(local *Model, localVendor, upstreamVendor *Vendor) string {
	encoded, _ := common.Marshal([]any{local, localVendor, upstreamVendor})
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func GetMetadataSyncState(db *gorm.DB) (map[string]*Model, map[string]*Vendor, error) {
	var models []*Model
	var vendors []*Vendor
	if err := db.Session(&gorm.Session{}).Order("id").Find(&models).Error; err != nil {
		return nil, nil, err
	}
	if err := db.Session(&gorm.Session{}).Order("id").Find(&vendors).Error; err != nil {
		return nil, nil, err
	}
	modelMap := make(map[string]*Model, len(models))
	vendorMap := make(map[string]*Vendor, len(vendors))
	for _, item := range models {
		modelMap[item.ModelName] = item
	}
	for _, item := range vendors {
		vendorMap[item.Name] = item
	}
	return modelMap, vendorMap, nil
}

func ValidateMetadataValues(values MetadataValues) error {
	if values.NameRule < NameRuleExact || values.NameRule > NameRuleSuffix {
		return errors.New("invalid metadata matching rule")
	}
	if values.Status != 0 && values.Status != 1 {
		return errors.New("invalid catalog visibility")
	}
	return ValidateModelEndpoints(values.Endpoints)
}

// ValidateModelEndpoints accepts the existing map form (custom paths) and
// type-array form (declared protocols), but never arbitrary JSON scalars.
func ValidateModelEndpoints(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var value any
	if err := common.UnmarshalJsonStr(raw, &value); err != nil {
		return fmt.Errorf("invalid endpoints: %w", err)
	}
	switch endpoints := value.(type) {
	case []any:
		for _, endpoint := range endpoints {
			if text, ok := endpoint.(string); !ok || strings.TrimSpace(text) == "" {
				return errors.New("endpoint types must be non-empty strings")
			}
		}
	case map[string]any:
		for key, endpoint := range endpoints {
			if strings.TrimSpace(key) == "" {
				return errors.New("endpoint type is required")
			}
			switch details := endpoint.(type) {
			case string:
				if !strings.HasPrefix(details, "/") {
					return errors.New("endpoint paths must start with /")
				}
			case map[string]any:
				path, _ := details["path"].(string)
				if !strings.HasPrefix(path, "/") {
					return errors.New("endpoint paths must start with /")
				}
				if method, exists := details["method"]; exists {
					switch method {
					case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
					default:
						return errors.New("invalid endpoint HTTP method")
					}
				}
			default:
				return errors.New("endpoint configuration must be a path or object")
			}
		}
	default:
		return errors.New("endpoints must be a JSON object or array")
	}
	return nil
}

func ApplyMetadataSync(updates []MetadataSyncUpdate, upstreamVendors map[string]Vendor) (*MetadataSyncResult, error) {
	if len(updates) == 0 {
		return nil, errors.New("select metadata changes before applying")
	}
	seen := make(map[string]bool)
	for _, update := range updates {
		if seen[update.ModelName] || strings.TrimSpace(update.ModelName) == "" {
			return nil, errors.New("invalid or duplicate model selection")
		}
		seen[update.ModelName] = true
		if update.RecordVersion == "" {
			return nil, ErrMetadataSyncConflict
		}
		if err := ValidateMetadataValues(update.Values); err != nil {
			return nil, err
		}
		if !update.Create && len(update.Fields) == 0 {
			return nil, errors.New("select fields to update")
		}
		allowed := make(map[string]bool)
		for _, field := range MetadataSyncFields {
			allowed[field] = true
		}
		for _, field := range update.Fields {
			if !allowed[field] {
				return nil, fmt.Errorf("unsupported metadata field: %s", field)
			}
		}
	}
	result := &MetadataSyncResult{CreatedModels: []string{}, UpdatedModels: []MetadataSyncSelection{}, CreatedVendors: []string{}}
	err := metadataTransaction(func(tx *gorm.DB) error {
		locals, vendors, err := GetMetadataSyncState(lockForUpdate(tx))
		if err != nil {
			return err
		}
		vendorByID := make(map[int]*Vendor)
		for _, vendor := range vendors {
			vendorByID[vendor.Id] = vendor
		}
		// Verify the entire selection before any write, including shared vendors.
		for _, update := range updates {
			local := locals[update.ModelName]
			if local != nil && local.SyncOfficial == 0 {
				return fmt.Errorf("metadata sync is disabled for %s", update.ModelName)
			}
			var localVendor *Vendor
			if local != nil {
				localVendor = vendorByID[local.VendorID]
			}
			if MetadataRecordVersion(local, localVendor, FindMetadataVendor(vendors, update.Values.Vendor)) != update.RecordVersion {
				return fmt.Errorf("%w: %s", ErrMetadataSyncConflict, update.ModelName)
			}
			if update.Create && local != nil || !update.Create && local == nil {
				return ErrMetadataSyncConflict
			}
		}
		for _, update := range updates {
			fields := make(map[string]any)
			selected := update.Fields
			if update.Create {
				selected = MetadataSyncFields
			}
			for _, field := range selected {
				switch field {
				case "description":
					fields[field] = update.Values.Description
				case "icon":
					fields[field] = update.Values.Icon
				case "tags":
					fields[field] = update.Values.Tags
				case "endpoints":
					fields[field] = update.Values.Endpoints
				case "name_rule":
					fields[field] = update.Values.NameRule
				case "status":
					fields[field] = update.Values.Status
				case "vendor":
					vendorID := 0
					name := update.Values.Vendor
					if name != "" {
						vendor := FindMetadataVendor(vendors, name)
						if vendor == nil {
							up, ok := upstreamVendors[name]
							if !ok {
								return fmt.Errorf("upstream vendor not found: %s", name)
							}
							vendor = &Vendor{Name: name, Description: up.Description, Icon: up.Icon, Status: 1, CreatedTime: common.GetTimestamp(), UpdatedTime: common.GetTimestamp()}
							if err := validateVendorMetadata(tx, vendor); err != nil {
								return err
							}
							if err := tx.Create(vendor).Error; err != nil {
								return err
							}
							vendors[name] = vendor
							result.CreatedVendors = append(result.CreatedVendors, name)
						}
						vendorID = vendor.Id
					}
					fields["vendor_id"] = vendorID
				}
			}
			fields["updated_time"] = common.GetTimestamp()
			if update.Create {
				fields["model_name"] = update.ModelName
				fields["sync_official"] = 1
				fields["created_time"] = common.GetTimestamp()
				if err := tx.Model(&Model{}).Create(fields).Error; err != nil {
					return err
				}
				result.CreatedModels = append(result.CreatedModels, update.ModelName)
			} else {
				if err := tx.Model(&Model{}).Where("id = ?", locals[update.ModelName].Id).Updates(fields).Error; err != nil {
					return err
				}
				result.UpdatedModels = append(result.UpdatedModels, update.MetadataSyncSelection)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	RefreshPricing()
	return result, nil
}
