// Package util provides helper functions for SSH tunnel instructions and network-related tasks.
// This includes detecting the appropriate IP address and printing commands
// to help users connect to the local server from a remote machine.
package util

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

var ipServices = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
	"https://icanhazip.com",
	"https://ipinfo.io/ip",
}

// getPublicIP attempts to retrieve the public IP address from a list of external services.
// It iterates through the ipServices and returns the first successful response.
//
// Returns:
//   - string: The public IP address as a string
//   - error: An error if all services fail, nil otherwise
// ipCheckClient is a shared HTTP client for IP detection services.
var ipCheckClient = &http.Client{Timeout: 5 * time.Second}

func getPublicIP() (string, error) {
	for _, service := range ipServices {
		if ip, err := fetchIPFromService(service); err != nil {
			log.Debugf("%v", err)
		} else if ip != "" {
			return ip, nil
		}
	}
	return "", fmt.Errorf("all IP services failed")
}

func fetchIPFromService(service string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", service, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request to %s: %w", service, err)
	}

	resp, err := ipCheckClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to get public IP from %s: %w", service, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bad status code from %s: %d", service, resp.StatusCode)
	}

	ip, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body from %s: %w", service, err)
	}
	return strings.TrimSpace(string(ip)), nil
}

// getOutboundIP retrieves the preferred outbound IP address of this machine.
// It uses a UDP connection to a public DNS server to determine the local IP
// address that would be used for outbound traffic.
//
// Returns:
//   - string: The outbound IP address as a string
//   - error: An error if the IP address cannot be determined, nil otherwise
func getOutboundIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			log.Warnf("Failed to close UDP connection: %v", closeErr)
		}
	}()

	localAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", fmt.Errorf("could not assert UDP address type")
	}

	return localAddr.IP.String(), nil
}

// GetIPAddress attempts to find the best-available IP address.
// It first tries to get the public IP address, and if that fails,
// it falls back to getting the local outbound IP address.
//
// Returns:
//   - string: The determined IP address (preferring public IPv4)
func GetIPAddress() string {
	publicIP, err := getPublicIP()
	if err == nil {
		log.Debugf("Public IP detected: %s", publicIP)
		return publicIP
	}
	log.Warnf("Failed to get public IP, falling back to outbound IP: %v", err)
	outboundIP, err := getOutboundIP()
	if err == nil {
		log.Debugf("Outbound IP detected: %s", outboundIP)
		return outboundIP
	}
	log.Errorf("Failed to get any IP address: %v", err)
	return "127.0.0.1" // Fallback
}

// PrintSSHTunnelInstructions detects the IP address and prints SSH tunnel instructions
// for the user to connect to the local OAuth callback server from a remote machine.
//
// Parameters:
//   - port: The local port number for the SSH tunnel
func PrintSSHTunnelInstructions(port int) {
	ipAddress := GetIPAddress()
	border := "================================================================================"
	fmt.Println("To authenticate from a remote machine, an SSH tunnel may be required.")
	fmt.Println(border)
	fmt.Println("  Run one of the following commands on your local machine (NOT the server):")
	fmt.Println()
	fmt.Printf("  # Standard SSH command (assumes SSH port 22):\n")
	fmt.Printf("  ssh -L %d:127.0.0.1:%d root@%s -p 22\n", port, port, ipAddress)
	fmt.Println()
	fmt.Printf("  # If using an SSH key (assumes SSH port 22):\n")
	fmt.Printf("  ssh -i <path_to_your_key> -L %d:127.0.0.1:%d root@%s -p 22\n", port, port, ipAddress)
	fmt.Println()
	fmt.Println("  NOTE: If your server's SSH port is not 22, please modify the '-p 22' part accordingly.")
	fmt.Println(border)
}
