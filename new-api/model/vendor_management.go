package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrVendorConflict = errors.New("vendor data changed; preview again before applying")

// VendorReferenceError reports every referenced vendor in a rejected delete.
type VendorReferenceError struct {
	Counts map[int]int64 `json:"reference_counts"`
}

func (e *VendorReferenceError) Error() string {
	return "vendors are still referenced by models; transfer or clear their assignments first"
}

func FindMetadataVendor(vendors map[string]*Vendor, name string) *Vendor {
	for _, vendor := range vendors {
		if strings.EqualFold(strings.TrimSpace(vendor.Name), strings.TrimSpace(name)) {
			return vendor
		}
	}
	return nil
}

func validateModelVendor(tx *gorm.DB, id int) error {
	if id == 0 {
		return nil
	}
	if id < 0 {
		return errors.New("select a saved vendor")
	}
	var count int64
	if err := tx.Model(&Vendor{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return errors.New("vendor does not exist")
	}
	return nil
}

func validateVendorMetadata(tx *gorm.DB, vendor *Vendor) error {
	vendor.Name = strings.TrimSpace(vendor.Name)
	vendor.Icon = strings.TrimSpace(vendor.Icon)
	if vendor.Name == "" {
		return errors.New("vendor name is required")
	}
	if utf8.RuneCountInString(vendor.Name) > 128 || utf8.RuneCountInString(vendor.Icon) > 128 {
		return errors.New("vendor name and icon must not exceed 128 characters")
	}
	var others []Vendor
	if err := tx.Select("id", "name").Where("id <> ?", vendor.Id).Find(&others).Error; err != nil {
		return err
	}
	for _, other := range others {
		if strings.EqualFold(strings.TrimSpace(other.Name), vendor.Name) {
			return errors.New("vendor name already exists")
		}
	}
	return nil
}

func VendorRecordVersion(v *Vendor) string {
	encoded, _ := common.Marshal([]any{v.Id, v.Name, v.Description, v.Icon, v.Status, v.CreatedTime, v.UpdatedTime})
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

// VendorOperation uses explicit selections for both preview and application.
type VendorOperation struct {
	Action          string `json:"action"`
	VendorIDs       []int  `json:"vendor_ids"`
	ModelIDs        []int  `json:"model_ids"`
	TargetVendorID  int    `json:"target_vendor_id"`
	ExpectedVersion string `json:"expected_version,omitempty"`
}

type VendorAssignmentModel struct {
	ID          int    `json:"id"`
	ModelName   string `json:"model_name"`
	NameRule    int    `json:"name_rule"`
	VendorID    int    `json:"vendor_id"`
	VendorName  string `json:"vendor_name"`
	UpdatedTime int64  `json:"updated_time"`
}

type VendorOperationPreview struct {
	Action  string                  `json:"action"`
	Sources []Vendor                `json:"sources"`
	Target  *Vendor                 `json:"target"`
	Models  []VendorAssignmentModel `json:"models"`
	Version string                  `json:"version"`
}

type VendorOperationResult struct {
	UpdatedModels  []int `json:"updated_models"`
	DeletedVendors []int `json:"deleted_vendors"`
}

func buildVendorOperationPreview(db *gorm.DB, operation VendorOperation) (*VendorOperationPreview, error) {
	if operation.Action != "assign" && operation.Action != "merge" && operation.Action != "delete" {
		return nil, errors.New("unsupported vendor operation")
	}
	ids := append([]int{}, operation.VendorIDs...)
	if operation.Action == "assign" {
		ids = append([]int{}, operation.ModelIDs...)
	}
	if len(ids) == 0 || len(ids) > 1000 {
		return nil, errors.New("select between 1 and 1000 records")
	}
	slices.Sort(ids)
	for i, id := range ids {
		if id <= 0 || i > 0 && ids[i-1] == id {
			return nil, errors.New("invalid or duplicate selection")
		}
	}
	preview := &VendorOperationPreview{Action: operation.Action, Sources: []Vendor{}, Models: []VendorAssignmentModel{}}
	var vendors []Vendor
	if err := db.Session(&gorm.Session{}).Order("id").Find(&vendors).Error; err != nil {
		return nil, err
	}
	byID := make(map[int]*Vendor, len(vendors))
	for i := range vendors {
		byID[vendors[i].Id] = &vendors[i]
	}
	if operation.Action != "delete" {
		if operation.TargetVendorID < 0 || operation.Action == "merge" && operation.TargetVendorID == 0 {
			return nil, errors.New("select a saved target vendor")
		}
		if operation.TargetVendorID != 0 {
			preview.Target = byID[operation.TargetVendorID]
			if preview.Target == nil {
				return nil, fmt.Errorf("%w: target vendor does not exist", ErrVendorConflict)
			}
		}
	}
	var models []Model
	query := db.Session(&gorm.Session{}).Model(&Model{}).Order("id")
	if operation.Action == "assign" {
		query = query.Where("id IN ?", ids)
	} else {
		for _, id := range ids {
			vendor := byID[id]
			if vendor == nil {
				return nil, fmt.Errorf("%w: source vendor does not exist", ErrVendorConflict)
			}
			if operation.Action == "merge" && id == operation.TargetVendorID {
				return nil, errors.New("target vendor cannot also be a source")
			}
			preview.Sources = append(preview.Sources, *vendor)
		}
		query = query.Where("vendor_id IN ?", ids)
	}
	if err := query.Find(&models).Error; err != nil {
		return nil, err
	}
	if operation.Action == "assign" && len(models) != len(ids) {
		return nil, fmt.Errorf("%w: a selected model no longer exists", ErrVendorConflict)
	}
	seen := make(map[int]bool)
	modelVersions := make([]string, 0, len(models))
	counts := make(map[int]int64)
	for _, item := range models {
		modelVersions = append(modelVersions, MetadataRecordVersion(&item, nil, nil))
		row := VendorAssignmentModel{ID: item.Id, ModelName: item.ModelName, NameRule: item.NameRule, VendorID: item.VendorID, UpdatedTime: item.UpdatedTime}
		if vendor := byID[item.VendorID]; vendor != nil {
			row.VendorName = vendor.Name
			if operation.Action == "assign" && !seen[vendor.Id] {
				preview.Sources = append(preview.Sources, *vendor)
				seen[vendor.Id] = true
			}
		}
		preview.Models = append(preview.Models, row)
		counts[item.VendorID]++
	}
	if operation.Action == "delete" && len(models) > 0 {
		return nil, &VendorReferenceError{Counts: counts}
	}
	slices.SortFunc(preview.Sources, func(a, b Vendor) int { return a.Id - b.Id })
	encoded, err := common.Marshal([]any{preview, modelVersions})
	if err != nil {
		return nil, err
	}
	preview.Version = fmt.Sprintf("%x", sha256.Sum256(encoded))
	return preview, nil
}

func PreviewVendorOperation(operation VendorOperation) (*VendorOperationPreview, error) {
	return buildVendorOperationPreview(DB, operation)
}

func ApplyVendorOperation(operation VendorOperation) (*VendorOperationResult, error) {
	if operation.ExpectedVersion == "" {
		return nil, ErrVendorConflict
	}
	return applyVendorOperation(operation)
}

func DeleteVendors(ids []int) error {
	_, err := applyVendorOperation(VendorOperation{Action: "delete", VendorIDs: ids})
	return err
}

func applyVendorOperation(operation VendorOperation) (*VendorOperationResult, error) {
	result := &VendorOperationResult{UpdatedModels: []int{}, DeletedVendors: []int{}}
	err := metadataTransaction(func(tx *gorm.DB) error {
		preview, err := buildVendorOperationPreview(lockForUpdate(tx), operation)
		if err != nil {
			return err
		}
		if operation.ExpectedVersion != "" && operation.ExpectedVersion != preview.Version {
			return ErrVendorConflict
		}
		for _, item := range preview.Models {
			if item.VendorID != operation.TargetVendorID {
				result.UpdatedModels = append(result.UpdatedModels, item.ID)
			}
		}
		if len(result.UpdatedModels) > 0 {
			if err := tx.Model(&Model{}).Where("id IN ?", result.UpdatedModels).Updates(map[string]any{"vendor_id": operation.TargetVendorID, "updated_time": common.GetTimestamp()}).Error; err != nil {
				return err
			}
		}
		if operation.Action == "merge" || operation.Action == "delete" {
			for _, vendor := range preview.Sources {
				result.DeletedVendors = append(result.DeletedVendors, vendor.Id)
			}
			if err := tx.Where("id IN ?", result.DeletedVendors).Delete(&Vendor{}).Error; err != nil {
				return err
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
