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
func getPublicIP() (string, error) {
	for _, service := range ipServices {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", service, nil)
		if err != nil {
			log.Debugf("Failed to create request to %s: %v", service, err)
			continue
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Debugf("Failed to get public IP from %s: %v", service, err)
			continue
		}
		defer func() {
			if closeErr := resp.Body.Close(); closeErr != nil {
				log.Warnf("Failed to close response body from %s: %v", service, closeErr)
			}
		}()

		if resp.StatusCode != http.StatusOK {
			log.Debugf("bad status code from %s: %d", service, resp.StatusCode)
			continue
		}

		ip, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Debugf("Failed to read response body from %s: %v", service, err)
			continue
		}
		return strings.TrimSpace(string(ip)), nil
	}
	return "", fmt.Errorf("all IP services failed")
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
