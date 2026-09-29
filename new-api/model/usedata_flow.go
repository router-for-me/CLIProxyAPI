package model

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type FlowQuotaData struct {
	UserID      int    `json:"user_id,omitempty" gorm:"column:user_id"`
	Username    string `json:"username,omitempty" gorm:"column:username"`
	NodeName    string `json:"node_name,omitempty" gorm:"column:node_name"`
	TokenID     int    `json:"token_id,omitempty" gorm:"column:token_id"`
	TokenName   string `json:"token_name,omitempty" gorm:"-"`
	UseGroup    string `json:"use_group" gorm:"column:use_group"`
	ChannelID   int    `json:"channel_id,omitempty" gorm:"column:channel_id"`
	ChannelName string `json:"channel_name,omitempty" gorm:"-"`
	ModelName   string `json:"model_name" gorm:"column:model_name"`
	TokenUsed   int    `json:"token_used" gorm:"column:token_used"`
	Count       int    `json:"count" gorm:"column:count"`
	Quota       int    `json:"quota" gorm:"column:quota"`
}

func GetFlowQuotaData(startTime int64, endTime int64, username string, userID int, role int) ([]*FlowQuotaData, error) {
	switch {
	case role >= common.RoleRootUser:
		return getRootFlowQuotaData(startTime, endTime, username)
	case role >= common.RoleAdminUser:
		return getAdminFlowQuotaData(startTime, endTime, username)
	default:
		return getSelfFlowQuotaData(startTime, endTime, userID)
	}
}

func flowQuotaBaseQuery(startTime int64, endTime int64) *gorm.DB {
	query := DB.Table("quota_data").
		Where("use_group <> ''").
		Where("created_at >= ? and created_at <= ?", startTime, endTime)
	return query
}

func getSelfFlowQuotaData(startTime int64, endTime int64, userID int) ([]*FlowQuotaData, error) {
	rows := make([]*FlowQuotaData, 0)
	err := flowQuotaBaseQuery(startTime, endTime).
		Select("token_id, use_group, model_name, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used").
		Where("user_id = ?", userID).
		Group("token_id, use_group, model_name").
		Order("quota DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, fillFlowTokenNames(rows)
}

func getAdminFlowQuotaData(startTime int64, endTime int64, username string) ([]*FlowQuotaData, error) {
	rows := make([]*FlowQuotaData, 0)
	query := flowQuotaBaseQuery(startTime, endTime).
		Select("user_id, username, use_group, model_name, channel_id, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used")
	if username != "" {
		query = query.Where("username = ?", username)
	}
	err := query.
		Group("user_id, username, use_group, model_name, channel_id").
		Order("quota DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, fillFlowChannelNames(rows)
}

func getRootFlowQuotaData(startTime int64, endTime int64, username string) ([]*FlowQuotaData, error) {
	rows := make([]*FlowQuotaData, 0)
	query := flowQuotaBaseQuery(startTime, endTime).
		Select("user_id, username, node_name, token_id, use_group, model_name, channel_id, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used")
	if username != "" {
		query = query.Where("username = ?", username)
	}
	err := query.
		Group("user_id, username, node_name, token_id, use_group, model_name, channel_id").
		Order("quota DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if err := fillFlowTokenNames(rows); err != nil {
		return rows, err
	}
	return rows, fillFlowChannelNames(rows)
}

func fillFlowTokenNames(rows []*FlowQuotaData) error {
	tokenIDSet := make(map[int]struct{})
	tokenIDs := make([]int, 0)
	for _, row := range rows {
		if row.TokenID == 0 {
			continue
		}
		if _, ok := tokenIDSet[row.TokenID]; ok {
			continue
		}
		tokenIDSet[row.TokenID] = struct{}{}
		tokenIDs = append(tokenIDs, row.TokenID)
	}
	if len(tokenIDs) == 0 {
		return nil
	}

	var tokens []struct {
		Id   int    `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	if err := DB.Model(&Token{}).Select("id, name").Where("id IN ?", tokenIDs).Find(&tokens).Error; err != nil {
		return err
	}
	tokenNameByID := make(map[int]string, len(tokens))
	for _, token := range tokens {
		tokenNameByID[token.Id] = token.Name
	}
	// Deleted tokens are intentionally not resolved here: leave TokenName empty
	// so the frontend can render a localized "deleted (id)" label instead.
	for _, row := range rows {
		if name := tokenNameByID[row.TokenID]; name != "" {
			row.TokenName = name
		}
	}
	return nil
}

func fillFlowChannelNames(rows []*FlowQuotaData) error {
	channelIDSet := make(map[int]struct{})
	channelIDs := make([]int, 0)
	for _, row := range rows {
		if row.ChannelID == 0 {
			continue
		}
		if _, ok := channelIDSet[row.ChannelID]; ok {
			continue
		}
		channelIDSet[row.ChannelID] = struct{}{}
		channelIDs = append(channelIDs, row.ChannelID)
	}
	if len(channelIDs) == 0 {
		return nil
	}

	channelNameByID := make(map[int]string, len(channelIDs))
	if common.MemoryCacheEnabled {
		for _, channelID := range channelIDs {
			if channel, err := CacheGetChannel(channelID); err == nil {
				channelNameByID[channelID] = channel.Name
			}
		}
	} else {
		var channels []struct {
			Id   int    `gorm:"column:id"`
			Name string `gorm:"column:name"`
		}
		if err := DB.Table("channels").Select("id, name").Where("id IN ?", channelIDs).Find(&channels).Error; err != nil {
			return err
		}
		for _, channel := range channels {
			channelNameByID[channel.Id] = channel.Name
		}
	}
	for _, row := range rows {
		if name := channelNameByID[row.ChannelID]; name != "" {
			row.ChannelName = name
			continue
		}
		if row.ChannelID > 0 {
			row.ChannelName = fmt.Sprintf("channel-%d", row.ChannelID)
		}
	}
	return nil
}
