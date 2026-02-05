package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/codex-lite/internal/config"
	"github.com/codex-lite/internal/manager"
	"github.com/codex-lite/internal/proxy"
	"github.com/codex-lite/internal/web"
	"github.com/gin-gonic/gin"
)

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatal(err)
	}

	mgr := manager.NewManager(cfg.AuthDir)
	if err := mgr.Load(); err != nil {
		log.Fatal(err)
	}

	executor := proxy.NewExecutor(cfg.Proxy.Timeout)
	proxyHandler := proxy.NewHandler(executor, mgr)
	webHandler := web.NewHandler(mgr, cfg.AuthDir, cfg.OAuth.CallbackPort)

	r := gin.Default()
	setupRoutes(r, proxyHandler, webHandler)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("Starting server on %s", addr)
	r.Run(addr)
}

func setupRoutes(r *gin.Engine, proxyHandler *proxy.Handler, webHandler *web.Handler) {
	// 静态文件服务
	r.Static("/static", "./web/static")
	r.StaticFile("/", "./web/static/index.html")
	r.StaticFile("/accounts.html", "./web/static/accounts.html")

	// Proxy API - OpenAI 兼容接口
	v1 := r.Group("/v1")
	{
		v1.POST("/chat/completions", handleChatCompletions(proxyHandler))
		v1.POST("/responses", proxyHandler.ChatCompletions)
	}

	// Management API - 管理接口
	api := r.Group("/api")
	{
		api.GET("/status", webHandler.GetStatus)
		api.GET("/accounts", webHandler.ListAccounts)
		api.POST("/auth/login", webHandler.StartLogin)
		api.GET("/auth/callback", webHandler.HandleCallback)
		api.POST("/accounts/:email/refresh", webHandler.RefreshAccount)
	}
}

// handleChatCompletions 处理 OpenAI 格式的请求
func handleChatCompletions(h *proxy.Handler) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 检查是否为流式请求
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var req map[string]interface{}
		if err := json.Unmarshal(body, &req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// 重新设置 body 供 handler 使用
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		if stream, ok := req["stream"].(bool); ok && stream {
			h.ChatCompletionsStream(c)
		} else {
			h.ChatCompletions(c)
		}
	}
}
