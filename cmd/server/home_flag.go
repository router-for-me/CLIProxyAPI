package main

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func parseHomeFlagConfig(rawAddr string, password string) (config.HomeConfig, error) {
	rawAddr = strings.TrimSpace(rawAddr)
	if rawAddr == "" {
		return config.HomeConfig{}, fmt.Errorf("address is empty")
	}

	if strings.Contains(rawAddr, "://") {
		return parseHomeURLConfig(rawAddr, password)
	}

	host, portStr, errSplit := net.SplitHostPort(rawAddr)
	if errSplit != nil {
		return config.HomeConfig{}, fmt.Errorf("expected host:port, redis://host:port, or rediss://host:port: %w", errSplit)
	}

	host = strings.TrimSpace(host)
	if host == "" {
		return config.HomeConfig{}, fmt.Errorf("host is empty")
	}

	port, errPort := parseHomePort(portStr)
	if errPort != nil {
		return config.HomeConfig{}, errPort
	}

	return config.HomeConfig{
		Enabled:  true,
		Host:     host,
		Port:     port,
		Password: password,
	}, nil
}

func parseHomeURLConfig(rawAddr string, password string) (config.HomeConfig, error) {
	parsed, errParse := url.Parse(rawAddr)
	if errParse != nil {
		return config.HomeConfig{}, fmt.Errorf("parse URL: %w", errParse)
	}

	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "redis" && scheme != "rediss" {
		return config.HomeConfig{}, fmt.Errorf("unsupported URL scheme %q", parsed.Scheme)
	}

	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return config.HomeConfig{}, fmt.Errorf("host is empty")
	}

	port, errPort := parseHomePort(parsed.Port())
	if errPort != nil {
		return config.HomeConfig{}, errPort
	}

	if password == "" && parsed.User != nil {
		if urlPassword, ok := parsed.User.Password(); ok {
			password = urlPassword
		}
	}

	homeCfg := config.HomeConfig{
		Enabled:  true,
		Host:     host,
		Port:     port,
		Password: password,
	}

	if scheme == "rediss" {
		homeCfg.TLS.Enable = true
		query := parsed.Query()
		homeCfg.TLS.ServerName = strings.TrimSpace(firstHomeQueryValue(query, "server-name", "server_name"))
		homeCfg.TLS.InsecureSkipVerify = parseHomeBoolQuery(query, "insecure-skip-verify", "insecure_skip_verify", "skip_verify")
		homeCfg.TLS.CACert = strings.TrimSpace(firstHomeQueryValue(query, "ca-cert", "ca_cert"))
	}

	return homeCfg, nil
}

func parseHomePort(rawPort string) (int, error) {
	rawPort = strings.TrimSpace(rawPort)
	if rawPort == "" {
		return 0, fmt.Errorf("port is empty")
	}

	port, errPort := strconv.Atoi(rawPort)
	if errPort != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("invalid port %q", rawPort)
	}

	return port, nil
}

func firstHomeQueryValue(values url.Values, keys ...string) string {
	for _, key := range keys {
		if value := values.Get(key); value != "" {
			return value
		}
	}
	return ""
}

func parseHomeBoolQuery(values url.Values, keys ...string) bool {
	for _, key := range keys {
		value := strings.TrimSpace(values.Get(key))
		if value == "" {
			continue
		}
		parsed, errParse := strconv.ParseBool(value)
		return errParse == nil && parsed
	}
	return false
}
