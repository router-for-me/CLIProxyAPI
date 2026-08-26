package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/proxy"
)

// TestProxyResponse is the JSON response for the test-proxy endpoint.
type TestProxyResponse struct {
	Success bool   `json:"success"`
	IP      string `json:"ip,omitempty"`
	Country string `json:"country,omitempty"`
	Error   string `json:"error,omitempty"`
}

// TorRotateResponse is the JSON response for the tor-rotate endpoint.
type TorRotateResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	OldIP   string `json:"old_ip,omitempty"`
	NewIP   string `json:"new_ip,omitempty"`
	Error   string `json:"error,omitempty"`
}

// TestProxy tests the proxy or Tor connection by fetching the external IP.
func (h *Handler) TestProxy(c *gin.Context) {
	cfg := h.cfg
	if cfg == nil {
		c.JSON(http.StatusInternalServerError, TestProxyResponse{
			Success: false,
			Error:   "config not available",
		})
		return
	}

	sdk := cfg
	if sdk == nil {
		c.JSON(http.StatusInternalServerError, TestProxyResponse{
			Success: false,
			Error:   "config not available",
		})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	var httpClient *http.Client

	switch sdk.ProxyMode {
	case "tor":
		// Use Tor SOCKS5 proxy
		torAddr := sdk.TorProxyAddr
		if torAddr == "" {
			torAddr = "127.0.0.1:9050"
		}
		if !strings.Contains(torAddr, ":") {
			torAddr = torAddr + ":9050"
		}

		dialer, err := proxy.SOCKS5("tcp", torAddr, nil, proxy.Direct)
		if err != nil {
			c.JSON(http.StatusOK, TestProxyResponse{
				Success: false,
				Error:   fmt.Sprintf("failed to create Tor SOCKS5 dialer: %v", err),
			})
			return
		}

		httpClient = &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				},
			},
		}

	default:
		// Regular proxy or direct
		proxyURL := sdk.ProxyURL
		if proxyURL != "" {
			transport, err := createProxyTransport(proxyURL)
			if err != nil {
				c.JSON(http.StatusOK, TestProxyResponse{
					Success: false,
					Error:   fmt.Sprintf("failed to create proxy transport: %v", err),
				})
				return
			}
			httpClient = &http.Client{
				Timeout:   15 * time.Second,
				Transport: transport,
			}
		} else {
			httpClient = &http.Client{Timeout: 15 * time.Second}
		}
	}

	// Try to fetch IP from ip-api.com (lightweight, no API key needed)
	ip, country, err := fetchExternalIP(ctx, httpClient)
	if err != nil {
		c.JSON(http.StatusOK, TestProxyResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to fetch external IP: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, TestProxyResponse{
		Success: true,
		IP:      ip,
		Country: country,
	})
}

// TorRotate sends a NEWNYM signal to Tor's control port to rotate the exit node IP.
func (h *Handler) TorRotate(c *gin.Context) {
	cfg := h.cfg
	if cfg == nil {
		c.JSON(http.StatusInternalServerError, TorRotateResponse{
			Success: false,
			Error:   "config not available",
		})
		return
	}

	controlAddr := cfg.TorControlAddr
	if controlAddr == "" {
		controlAddr = "127.0.0.1:9051"
	}
	if !strings.Contains(controlAddr, ":") {
		controlAddr = controlAddr + ":9051"
	}

	// Capture old IP before rotation
	oldIP := ""
	func() {
		torAddr := cfg.TorProxyAddr
		if torAddr == "" {
			torAddr = "127.0.0.1:9050"
		}
		if !strings.Contains(torAddr, ":") {
			torAddr = torAddr + ":9050"
		}
		dialer, err := proxy.SOCKS5("tcp", torAddr, nil, proxy.Direct)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		hc := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				},
			},
		}
		ip, _, err := fetchExternalIP(ctx, hc)
		if err == nil {
			oldIP = ip
		}
	}()

	// Connect to Tor control port
	conn, err := net.DialTimeout("tcp", controlAddr, 15*time.Second)
	if err != nil {
		c.JSON(http.StatusOK, TorRotateResponse{
			Success: false,
			Error:   fmt.Sprintf("cannot connect to Tor control port %s: %v", controlAddr, err),
		})
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(45 * time.Second))

	// Tor control protocol requires the client to send PROTOCOLINFO first
	// (Tor does not send unsolicited greetings on connect)
	_, err = conn.Write([]byte("PROTOCOLINFO\r\n"))
	if err != nil {
		c.JSON(http.StatusOK, TorRotateResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to send PROTOCOLINFO: %v", err),
		})
		return
	}

	// Read PROTOCOLINFO response
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		errMsg := fmt.Sprintf("failed to read Tor PROTOCOLINFO response: %v", err)
		if strings.Contains(err.Error(), "connection aborted") || strings.Contains(err.Error(), "reset by peer") {
			errMsg += ". Check that Tor ControlPort is not restricted to localhost " +
				"(add 'ControlPort 0.0.0.0:9051' to torrc on the Tor server and restart Tor)."
		}
		c.JSON(http.StatusOK, TorRotateResponse{
			Success: false,
			Error:   errMsg,
		})
		return
	}
	resp := string(buf[:n])
	if !strings.HasPrefix(resp, "250") {
		c.JSON(http.StatusOK, TorRotateResponse{
			Success: false,
			Error:   fmt.Sprintf("unexpected PROTOCOLINFO response: %s", resp),
		})
		return
	}

	// Send AUTHENTICATE with configured password (or empty if none)
	authCmd := "AUTHENTICATE \"\"\r\n"
	if cfg.TorControlPassword != "" {
		authCmd = fmt.Sprintf("AUTHENTICATE \"%s\"\r\n", cfg.TorControlPassword)
	}
	_, err = conn.Write([]byte(authCmd))
	if err != nil {
		c.JSON(http.StatusOK, TorRotateResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to send AUTHENTICATE: %v", err),
		})
		return
	}

	n, err = conn.Read(buf)
	if err != nil {
		c.JSON(http.StatusOK, TorRotateResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to read AUTHENTICATE response: %v", err),
		})
		return
	}
	resp = strings.TrimSpace(string(buf[:n]))
	if !strings.HasPrefix(resp, "250") {
		c.JSON(http.StatusOK, TorRotateResponse{
			Success: false,
			Error:   fmt.Sprintf("Tor AUTHENTICATE failed: %s", resp),
		})
		return
	}

	// Send SIGNAL NEWNYM
	_, err = conn.Write([]byte("SIGNAL NEWNYM\r\n"))
	if err != nil {
		c.JSON(http.StatusOK, TorRotateResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to send NEWNYM signal: %v", err),
		})
		return
	}

	n, err = conn.Read(buf)
	if err != nil {
		c.JSON(http.StatusOK, TorRotateResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to read NEWNYM response: %v", err),
		})
		return
	}
	resp = strings.TrimSpace(string(buf[:n]))
	if !strings.HasPrefix(resp, "250") {
		c.JSON(http.StatusOK, TorRotateResponse{
			Success: false,
			Error:   fmt.Sprintf("Tor NEWNYM failed: %s", resp),
		})
		return
	}

	// Wait a moment for Tor to establish new circuit
	time.Sleep(2 * time.Second)

	// Test the new IP
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	torAddr := cfg.TorProxyAddr
	if torAddr == "" {
		torAddr = "127.0.0.1:9050"
	}
	if !strings.Contains(torAddr, ":") {
		torAddr = torAddr + ":9050"
	}

	dialer, err := proxy.SOCKS5("tcp", torAddr, nil, proxy.Direct)
	if err != nil {
		c.JSON(http.StatusOK, TorRotateResponse{
			Success: true,
			Message: "NEWNYM signal sent but could not verify new IP",
		})
		return
	}

	httpClient := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
		},
	}

	newIP, _, err := fetchExternalIP(ctx, httpClient)
	if err != nil {
		c.JSON(http.StatusOK, TorRotateResponse{
			Success: true,
			Message: "NEWNYM signal sent, IP rotated successfully (could not verify)",
		})
		return
	}

	c.JSON(http.StatusOK, TorRotateResponse{
		Success: true,
		Message: "Tor IP rotated successfully",
		OldIP:   oldIP,
		NewIP:   newIP,
	})
}

// createProxyTransport creates an http.Transport that routes through the given proxy URL.
func createProxyTransport(proxyURL string) (*http.Transport, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return nil, nil
	}

	// Support socks5:// and http:// proxy URLs
	transport := &http.Transport{}

	if strings.HasPrefix(proxyURL, "socks5://") || strings.HasPrefix(proxyURL, "socks5h://") {
		// SOCKS5 proxy
		addr := strings.TrimPrefix(proxyURL, "socks5://")
		addr = strings.TrimPrefix(addr, "socks5h://")

		dialer, err := proxy.SOCKS5("tcp", addr, nil, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("failed to create SOCKS5 dialer: %v", err)
		}

		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		}
		return transport, nil
	}

	// HTTP/HTTPS proxy - use http.Transport.Proxy field
	transport = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}

	// Parse the custom proxy URL
	parsedURL, err := transport.Proxy(nil)
	if err == nil && parsedURL != nil {
		transport.Proxy = http.ProxyURL(parsedURL)
	}

	return transport, nil
}

// fetchExternalIP calls ip-api.com to get the external IP and country.
func fetchExternalIP(ctx context.Context, client *http.Client) (ip, country string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://ip-api.com/json/", nil)
	if err != nil {
		return "", "", fmt.Errorf("create request: %v", err)
	}
	req.Header.Set("User-Agent", "CLIProxyAPI/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("http request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("read response: %v", err)
	}

	var result struct {
		Status  string `json:"status"`
		Query   string `json:"query"`
		Country string `json:"country"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", fmt.Errorf("parse response: %v", err)
	}

	if result.Status != "success" {
		return "", "", fmt.Errorf("ip-api returned status: %s", result.Status)
	}

	return result.Query, result.Country, nil
}
