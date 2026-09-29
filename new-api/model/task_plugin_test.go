package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/utils/tests"
)

func setupTaskPluginModelTest(t *testing.T) {
	t.Helper()
	originalDB := DB
	t.Cleanup(func() { DB = originalDB })
	var err error
	DB, err = gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, DB.AutoMigrate(&TaskPlugin{}))
}

func TestTaskPluginVersionActivationAndSourceImmutability(t *testing.T) {
	setupTaskPluginModelTest(t)

	v1 := TaskPlugin{Key: "mock", APIVersion: 1, Version: "1.0.0", Source: "v1", SourceHash: "hash-v1", Enabled: true}
	require.NoError(t, SaveTaskPlugin(&v1))
	assert.True(t, v1.Active)

	v2 := TaskPlugin{Key: "mock", APIVersion: 1, Version: "2.0.0", Source: "v2", SourceHash: "hash-v2", Enabled: true}
	require.NoError(t, SaveTaskPlugin(&v2))
	assert.False(t, v2.Active)
	require.NoError(t, ActivateTaskPlugin("mock", "2.0.0"))

	active, err := ListActiveTaskPlugins()
	require.NoError(t, err)
	require.Len(t, active, 1)
	assert.Equal(t, "2.0.0", active[0].Version)

	conflict := TaskPlugin{Key: "mock", APIVersion: 1, Version: "2.0.0", Source: "changed", SourceHash: "different", Enabled: true}
	err = SaveTaskPlugin(&conflict)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "different source")

	require.NoError(t, SetTaskPluginEnabled("mock", false))
	active, err = ListActiveTaskPlugins()
	require.NoError(t, err)
	assert.Empty(t, active)

	all, err := ListTaskPlugins()
	require.NoError(t, err)
	assert.Len(t, all, 2)
	deleteResult, err := DeleteTaskPluginVersion("mock", "2.0.0")
	require.NoError(t, err)
	assert.True(t, deleteResult.DeletedActive)
	require.NotNil(t, deleteResult.Promoted)
	assert.Equal(t, "1.0.0", deleteResult.Promoted.Version)
	versions, err := ListTaskPluginVersions("mock")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	assert.Equal(t, "1.0.0", versions[0].Version)
	assert.True(t, versions[0].Active)
}

func TestDeleteActiveTaskPluginPromotesNewestRemainingVersion(t *testing.T) {
	setupTaskPluginModelTest(t)

	plugins := []*TaskPlugin{
		{Key: "promote", APIVersion: 1, Version: "1.0.0", Source: "v1", SourceHash: "hash-v1", Enabled: true},
		{Key: "promote", APIVersion: 1, Version: "2.0.0", Source: "v2", SourceHash: "hash-v2", Enabled: false},
		{Key: "promote", APIVersion: 1, Version: "3.0.0", Source: "v3", SourceHash: "hash-v3", Enabled: true},
		{Key: "promote", APIVersion: 1, Version: "4.0.0", Source: "v4", SourceHash: "hash-v4", Enabled: true},
	}
	for _, plugin := range plugins {
		require.NoError(t, SaveTaskPlugin(plugin))
	}
	require.NoError(t, DB.Model(plugins[1]).Update("created_at", 200).Error)
	require.NoError(t, DB.Model(plugins[2]).Update("created_at", 100).Error)
	require.NoError(t, DB.Model(plugins[3]).Update("created_at", 100).Error)

	deleteResult, err := DeleteTaskPluginVersion("promote", "1.0.0")
	require.NoError(t, err)
	assert.True(t, deleteResult.DeletedActive)
	require.NotNil(t, deleteResult.Promoted)
	assert.Equal(t, "2.0.0", deleteResult.Promoted.Version)
	assert.False(t, deleteResult.Promoted.Enabled)

	deleteResult, err = DeleteTaskPluginVersion("promote", "2.0.0")
	require.NoError(t, err)
	assert.True(t, deleteResult.DeletedActive)
	require.NotNil(t, deleteResult.Promoted)
	assert.Equal(t, "4.0.0", deleteResult.Promoted.Version)

	active, err := GetTaskPluginVersion("promote", "")
	require.NoError(t, err)
	assert.Equal(t, "4.0.0", active.Version)
}

func TestTaskPluginSyncSnapshotRevisionTracksDesiredRuntimeState(t *testing.T) {
	setupTaskPluginModelTest(t)

	empty, err := GetTaskPluginSyncSnapshot()
	require.NoError(t, err)
	assert.Empty(t, empty.Plugins)
	require.NotEmpty(t, empty.Revision)

	v1 := TaskPlugin{
		Key: "revision-probe", APIVersion: 1, Version: "1.0.0",
		Source: "v1", SourceHash: "hash-v1", Enabled: true,
	}
	require.NoError(t, SaveTaskPlugin(&v1))
	v1Snapshot, err := GetTaskPluginSyncSnapshot()
	require.NoError(t, err)
	require.Len(t, v1Snapshot.Plugins, 1)
	assert.NotEqual(t, empty.Revision, v1Snapshot.Revision)

	v2 := TaskPlugin{
		Key: "revision-probe", APIVersion: 1, Version: "2.0.0",
		Source: "v2", SourceHash: "hash-v2", Enabled: true,
	}
	require.NoError(t, SaveTaskPlugin(&v2))
	inactiveAdded, err := GetTaskPluginSyncSnapshot()
	require.NoError(t, err)
	assert.Equal(t, v1Snapshot.Revision, inactiveAdded.Revision)

	require.NoError(t, DB.Model(&v1).Update("remark", "operator note").Error)
	remarkChanged, err := GetTaskPluginSyncSnapshot()
	require.NoError(t, err)
	assert.Equal(t, v1Snapshot.Revision, remarkChanged.Revision)

	require.NoError(t, ActivateTaskPlugin("revision-probe", "2.0.0"))
	v2Snapshot, err := GetTaskPluginSyncSnapshot()
	require.NoError(t, err)
	require.Len(t, v2Snapshot.Plugins, 1)
	assert.Equal(t, "2.0.0", v2Snapshot.Plugins[0].Version)
	assert.NotEqual(t, v1Snapshot.Revision, v2Snapshot.Revision)

	require.NoError(t, SetTaskPluginEnabled("revision-probe", false))
	disabled, err := GetTaskPluginSyncSnapshot()
	require.NoError(t, err)
	assert.Empty(t, disabled.Plugins)
	assert.NotEqual(t, v2Snapshot.Revision, disabled.Revision)
}

func TestTaskPluginOrderSQLQuotesMySQLKeyColumn(t *testing.T) {
	db, err := gorm.Open(tests.DummyDialector{}, &gorm.Config{DryRun: true})
	require.NoError(t, err)

	var sqls []string
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:capture_task_plugin_sql", func(tx *gorm.DB) {
		sqls = append(sqls, tx.Statement.SQL.String())
	}))

	originalDB := DB
	t.Cleanup(func() { DB = originalDB })
	DB = db

	_, err = ListTaskPlugins()
	require.NoError(t, err)
	_, err = GetTaskPluginSyncSnapshot()
	require.NoError(t, err)

	require.Len(t, sqls, 2)
	for _, sql := range sqls {
		assert.Contains(t, sql, "`key`")
		assert.NotRegexp(t, `(?i)ORDER BY[[:space:]]+key([[:space:],]|$)`, sql)
	}
}
