package openai

import (
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain sets gin.TestMode once before any test runs. The per-test
// gin.SetMode(gin.TestMode) calls were removed because they race: parallel
// tests write the package-global gin mode while other parallel tests read it
// through gin.New/CreateTestContext. Setting it once up front removes the
// concurrent write.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}
