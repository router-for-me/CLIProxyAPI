package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetTaskPluginOptionsAdminForbiddenRootAllowed(t *testing.T) {
	wasMaster := common.IsMasterNode
	common.IsMasterNode = true
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.CasbinRule{}, &model.AuthzRole{}))
	require.NoError(t, authz.Init(db))
	t.Cleanup(func() { common.IsMasterNode = wasMaster })

	gin.SetMode(gin.TestMode)
	for _, testCase := range []struct {
		name       string
		id         int
		role       int
		wantStatus int
	}{
		{name: "admin", id: 2, role: common.RoleAdminUser, wantStatus: http.StatusForbidden},
		{name: "root", id: 1, role: common.RoleRootUser, wantStatus: http.StatusOK},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodGet, "/api/task_plugin_options", nil)
			context.Set("id", testCase.id)
			context.Set("role", testCase.role)
			middleware.RequirePermission(authz.TaskPluginBind)(context)
			if !context.IsAborted() {
				controller.GetTaskPluginOptions(context)
			}
			assert.Equal(t, testCase.wantStatus, recorder.Code)
		})
	}
}
