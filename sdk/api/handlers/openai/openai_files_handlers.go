package openai

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	cliproxyfiles "github.com/router-for-me/CLIProxyAPI/v6/internal/files"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
)

type OpenAIFilesAPIHandler struct {
	store *cliproxyfiles.Store
}

func NewOpenAIFilesAPIHandler(store *cliproxyfiles.Store) *OpenAIFilesAPIHandler {
	return &OpenAIFilesAPIHandler{store: store}
}

func (h *OpenAIFilesAPIHandler) Create(c *gin.Context) {
	if h == nil || h.store == nil {
		writeFilesError(c, http.StatusServiceUnavailable, "file store unavailable")
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil || fileHeader == nil {
		writeFilesError(c, http.StatusBadRequest, "missing multipart file field 'file'")
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		writeFilesError(c, http.StatusBadRequest, fmt.Sprintf("open uploaded file: %v", err))
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		writeFilesError(c, http.StatusBadRequest, fmt.Sprintf("read uploaded file: %v", err))
		return
	}
	meta, err := h.store.Create(fileHeader.Filename, c.PostForm("purpose"), fileHeader.Header.Get("Content-Type"), data)
	if err != nil {
		writeFilesError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":         meta.ID,
		"object":     meta.Object,
		"bytes":      meta.Bytes,
		"created_at": meta.CreatedAt,
		"filename":   meta.Filename,
		"purpose":    meta.Purpose,
		"status":     meta.Status,
	})
}

func (h *OpenAIFilesAPIHandler) Get(c *gin.Context) {
	meta, err := h.lookup(c.Param("id"))
	if err != nil {
		writeLookupError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":         meta.ID,
		"object":     meta.Object,
		"bytes":      meta.Bytes,
		"created_at": meta.CreatedAt,
		"filename":   meta.Filename,
		"purpose":    meta.Purpose,
		"status":     meta.Status,
	})
}

func (h *OpenAIFilesAPIHandler) List(c *gin.Context) {
	if h == nil || h.store == nil {
		writeFilesError(c, http.StatusServiceUnavailable, "file store unavailable")
		return
	}
	items, err := h.store.List()
	if err != nil {
		writeFilesError(c, http.StatusInternalServerError, err.Error())
		return
	}
	data := make([]gin.H, 0, len(items))
	for _, meta := range items {
		data = append(data, gin.H{
			"id":         meta.ID,
			"object":     meta.Object,
			"bytes":      meta.Bytes,
			"created_at": meta.CreatedAt,
			"filename":   meta.Filename,
			"purpose":    meta.Purpose,
			"status":     meta.Status,
		})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

func (h *OpenAIFilesAPIHandler) Delete(c *gin.Context) {
	if h == nil || h.store == nil {
		writeFilesError(c, http.StatusServiceUnavailable, "file store unavailable")
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if err := h.store.Delete(id); err != nil {
		writeLookupError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "object": "file", "deleted": true})
}

func (h *OpenAIFilesAPIHandler) lookup(id string) (*cliproxyfiles.Metadata, error) {
	if h == nil || h.store == nil {
		return nil, fmt.Errorf("file store unavailable")
	}
	return h.store.Get(strings.TrimSpace(id))
}

func writeLookupError(c *gin.Context, err error) {
	if errors.Is(err, cliproxyfiles.ErrNotFound) {
		writeFilesError(c, http.StatusNotFound, "file not found")
		return
	}
	writeFilesError(c, http.StatusInternalServerError, err.Error())
}

func writeFilesError(c *gin.Context, status int, message string) {
	body := handlers.BuildErrorResponseBody(status, message)
	c.Data(status, "application/json", body)
}
