package middleware

import (
	"io"
	"mime"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/sjson"
)

// rewriteTaskPluginJSONModel patches the top-level JSON "model" field and
// replaces BodyStorage. Non-JSON bodies are left untouched.
func rewriteTaskPluginJSONModel(c *gin.Context, spelling string) error {
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil {
		return nil
	}
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") {
		return nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return err
	}
	raw, err := storage.Bytes()
	if err != nil {
		return err
	}
	patched, err := sjson.SetBytes(raw, "model", spelling)
	if err != nil {
		return err
	}
	newStorage, err := common.CreateBodyStorage(patched)
	if err != nil {
		return err
	}
	_ = storage.Close()
	c.Set(common.KeyBodyStorage, newStorage)
	c.Set(common.KeyRequestBody, nil)
	c.Request.Body = io.NopCloser(newStorage)
	c.Request.ContentLength = int64(len(patched))
	return nil
}
