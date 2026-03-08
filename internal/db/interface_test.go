package db_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// TestStoreInterfaceCompiles 验证 Store 接口可编译
func TestStoreInterfaceCompiles(t *testing.T) {
	// 编译时类型检查：确保接口定义无语法错误
	var _ db.Store = nil
	var _ db.UserStore = nil
	var _ db.QuotaStore = nil
	var _ db.CredentialStore = nil
	var _ db.SecurityStore = nil
	var _ db.SettingsStore = nil
	var _ db.StatsStore = nil
}
