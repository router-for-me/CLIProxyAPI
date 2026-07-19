package util

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// TorSendCommand sends a command to the TOR control port.
func TorSendCommand(addr, password, command string) error {
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("dial control: %w", err)
	}
	defer conn.Close()

	buf := make([]byte, 4096)

	var authCmd string
	if password != "" {
		authCmd = fmt.Sprintf("AUTHENTICATE \"%s\"\r\n", password)
	} else {
		authCmd = "AUTHENTICATE\r\n"
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := conn.Write([]byte(authCmd)); err != nil {
		return fmt.Errorf("auth write: %w", err)
	}
	n, _ := conn.Read(buf)
	resp := string(buf[:n])

	authOK := false
	for _, line := range strings.Split(resp, "\r\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "515") {
			return fmt.Errorf("auth failed: %s", line)
		}
		if strings.HasPrefix(line, "250 ") || line == "250 OK" {
			authOK = true
		}
	}
	if !authOK {
		return fmt.Errorf("auth failed (unexpected): %s", strings.TrimSpace(resp))
	}

	cmd := command + "\r\n"
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return fmt.Errorf("command write: %w", err)
	}
	n, _ = conn.Read(buf)
	resp = string(buf[:n])

	cmdOK := false
	for _, line := range strings.Split(resp, "\r\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "250 ") || line == "250 OK" {
			cmdOK = true
			break
		}
		if strings.HasPrefix(line, "5") || strings.HasPrefix(line, "4") {
			return fmt.Errorf("command failed: %s", line)
		}
	}
	if !cmdOK {
		return fmt.Errorf("command failed: %s", strings.TrimSpace(resp))
	}
	return nil
}
