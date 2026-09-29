package controller

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// GetPrefillGroups 获取预填组列表，可通过 ?type=xxx 过滤
func GetPrefillGroups(c *gin.Context) {
	groupType := c.Query("type")
	groups, err := model.GetAllPrefillGroups(groupType)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, groups)
}

// CreatePrefillGroup 创建新的预填组
func CreatePrefillGroup(c *gin.Context) {
	var g model.PrefillGroup
	if err := c.ShouldBindJSON(&g); err != nil {
		common.ApiError(c, err)
		return
	}
	if g.Name == "" || g.Type == "" {
		common.ApiErrorMsg(c, "组名称和类型不能为空")
		return
	}
	// 创建前检查名称
	if dup, err := model.IsPrefillGroupNameDuplicated(0, g.Name); err != nil {
		common.ApiError(c, err)
		return
	} else if dup {
		common.ApiErrorMsg(c, "组名称已存在")
		return
	}

	if err := g.Insert(); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, &g)
}

// UpdatePrefillGroup 更新预填组
func UpdatePrefillGroup(c *gin.Context) {
	var g model.PrefillGroup
	if err := c.ShouldBindJSON(&g); err != nil {
		common.ApiError(c, err)
		return
	}
	if g.Id == 0 {
		common.ApiErrorMsg(c, "缺少组 ID")
		return
	}
	// 名称冲突检查
	if dup, err := model.IsPrefillGroupNameDuplicated(g.Id, g.Name); err != nil {
		common.ApiError(c, err)
		return
	} else if dup {
		common.ApiErrorMsg(c, "组名称已存在")
		return
	}

	if err := g.Update(); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, &g)
}

// DeletePrefillGroup 删除预填组
func DeletePrefillGroup(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if err := model.DeletePrefillGroupByID(id); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}
