package management

import (
	"fmt"
	"io"
	"time"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// PostTorRotate sends SIGNAL NEWNYM to TOR control port to rotate IP.
func (h *Handler) PostTorRotate(c *gin.Context) {
	ctrl := strings.TrimSpace(h.cfg.TorControl)
	if ctrl == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tor-control not configured"})
		return
	}

	if err := util.TorSendCommand(ctrl, h.cfg.TorPassword, "SIGNAL NEWNYM"); err != nil {
		log.WithError(err).Error("TOR rotate failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("rotate failed: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "IP rotation signal sent"})
}

// GetTorCheckIP checks the current public IP through TOR proxy.
func (h *Handler) GetTorCheckIP(c *gin.Context) {
	proxyURL := strings.TrimSpace(h.cfg.TorProxy)
	if proxyURL == "" {
		proxyURL = strings.TrimSpace(h.cfg.ProxyURL)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	if proxyURL != "" {
		transport, _, err := proxyutil.BuildHTTPTransport(proxyURL)
		if err == nil && transport != nil {
			client.Transport = transport
		}
	}

	// Try multiple IP check services
	services := []string{
		"https://api.ipify.org?format=json",
		"https://ifconfig.me/ip",
		"https://icanhazip.com/",
	}
	var lastErr error
	for _, url := range services {
		body, err := fetchJSON(c, client, url)
		if err == nil {
			// ifconfig.me and icanhazip return plain text
			if !strings.HasPrefix(string(body), "{") {
				body = []byte(`{"ip":"` + strings.TrimSpace(string(body)) + `"}`)
			}
			c.Data(http.StatusOK, "application/json", body)
			return
		}
		lastErr = err
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("all IP checks failed: %v", lastErr)})
}

func fetchJSON(c *gin.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

