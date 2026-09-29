package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// GetAllVendors uses the same paged filters and counts as the search endpoint.
func GetAllVendors(c *gin.Context) { SearchVendors(c) }

func SearchVendors(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	vendors, total, err := model.SearchVendors(c.Query("keyword"), pageInfo.GetStartIdx(), pageInfo.GetPageSize(), c.Query("association"))
	if err != nil {
		vendorAPIError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(vendors)
	common.ApiSuccess(c, pageInfo)
}

// GetVendorMeta 根据 ID 获取供应商
func GetVendorMeta(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		vendorAPIError(c, err)
		return
	}
	v, err := model.GetVendorByID(id)
	if err != nil {
		vendorAPIError(c, err)
		return
	}
	common.ApiSuccess(c, v)
}

// CreateVendorMeta 新建供应商
func CreateVendorMeta(c *gin.Context) {
	var v model.Vendor
	if err := c.ShouldBindJSON(&v); err != nil {
		vendorAPIError(c, err)
		return
	}
	if err := v.Insert(); err != nil {
		vendorAPIError(c, err)
		return
	}
	recordManageAudit(c, "vendor.metadata.save", map[string]any{"vendor_id": v.Id, "name": v.Name})
	common.ApiSuccess(c, &v)
}

// UpdateVendorMeta 更新供应商
func UpdateVendorMeta(c *gin.Context) {
	var v model.Vendor
	if err := c.ShouldBindJSON(&v); err != nil {
		vendorAPIError(c, err)
		return
	}
	if v.Id == 0 {
		common.ApiErrorMsg(c, "缺少供应商 ID")
		return
	}
	if err := v.Update(); err != nil {
		vendorAPIError(c, err)
		return
	}
	recordManageAudit(c, "vendor.metadata.save", map[string]any{"vendor_id": v.Id, "name": v.Name})
	common.ApiSuccess(c, &v)
}

// DeleteVendorMeta 删除供应商
func DeleteVendorMeta(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		vendorAPIError(c, err)
		return
	}
	if err := model.DeleteVendors([]int{id}); err != nil {
		vendorAPIError(c, err)
		return
	}
	recordManageAudit(c, "vendor.metadata.delete", map[string]any{"vendor_id": id})
	common.ApiSuccess(c, nil)
}

func vendorAPIError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	payload := gin.H{"success": false, "message": err.Error()}
	var references *model.VendorReferenceError
	if errors.Is(err, model.ErrVendorConflict) {
		status = http.StatusConflict
		payload["code"] = "VENDOR_CONFLICT"
	}
	if errors.As(err, &references) {
		status = http.StatusConflict
		payload["code"] = "VENDOR_REFERENCED"
		payload["reference_counts"] = references.Counts
	}
	c.JSON(status, payload)
}

func PreviewVendorOperation(c *gin.Context) {
	var request model.VendorOperation
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		vendorAPIError(c, err)
		return
	}
	preview, err := model.PreviewVendorOperation(request)
	if err != nil {
		vendorAPIError(c, err)
		return
	}
	common.ApiSuccess(c, preview)
}

func ApplyVendorOperation(c *gin.Context) {
	var request model.VendorOperation
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		vendorAPIError(c, err)
		return
	}
	result, err := model.ApplyVendorOperation(request)
	if err != nil {
		vendorAPIError(c, err)
		return
	}
	recordManageAudit(c, "vendor."+request.Action, map[string]any{"source_vendor_ids": request.VendorIDs, "target_vendor_id": request.TargetVendorID, "updated_model_ids": result.UpdatedModels, "deleted_vendor_ids": result.DeletedVendors})
	common.ApiSuccess(c, result)
}
