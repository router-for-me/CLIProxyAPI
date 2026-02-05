package proxy

import (
	"io"
	"net/http"

	"github.com/codex-lite/internal/auth"
	"github.com/gin-gonic/gin"
)

// TokenPicker 定义获取 token 的接口
type TokenPicker interface {
	Pick() *auth.TokenStorage
}

type Handler struct {
	executor *Executor
	picker   TokenPicker
}

func NewHandler(executor *Executor, picker TokenPicker) *Handler {
	return &Handler{
		executor: executor,
		picker:   picker,
	}
}

func (h *Handler) ChatCompletions(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	token := h.picker.Pick()
	if token == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no available account"})
		return
	}

	resp, err := h.executor.Execute(c.Request.Context(), token, body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.Data(http.StatusOK, "application/json", resp)
}

func (h *Handler) ChatCompletionsStream(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	token := h.picker.Pick()
	if token == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no available account"})
		return
	}

	stream, err := h.executor.ExecuteStream(c.Request.Context(), token, body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	for line := range stream {
		c.Writer.Write(line)
		c.Writer.Write([]byte("\n"))
		c.Writer.Flush()
	}
}
