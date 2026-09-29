package relay

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestGetTaskPlatformPriority(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	c.Set("platform", "fallback")
	assert.Equal(t, "fallback", string(GetTaskPlatform(c)))
	c.Set("channel_type", 59)
	assert.Equal(t, "59", string(GetTaskPlatform(c)))
	c.Set("task_plugin_key", "document-parser")
	assert.Equal(t, "document-parser", string(GetTaskPlatform(c)))
}
