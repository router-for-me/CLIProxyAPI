package qoder

import (
	"context"
	"net/url"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestResolveEndpointsGlobal(t *testing.T) {
	endpoints, err := ResolveEndpoints(&config.Config{})
	if err != nil {
		t.Fatalf("ResolveEndpoints() error = %v", err)
	}
	if endpoints.InferBase != QoderChatBase {
		t.Fatalf("InferBase = %q, want %q", endpoints.InferBase, QoderChatBase)
	}
	if endpoints.LoginURL() != QoderLoginURL {
		t.Fatalf("LoginURL() = %q, want %q", endpoints.LoginURL(), QoderLoginURL)
	}
	if endpoints.DeviceClientID() != "" {
		t.Fatalf("DeviceClientID() = %q, want empty for global endpoints", endpoints.DeviceClientID())
	}
	if endpoints.ModelListURL() != QoderModelListURL {
		t.Fatalf("ModelListURL() = %q, want %q", endpoints.ModelListURL(), QoderModelListURL)
	}
}

func TestResolveEndpointsVPC(t *testing.T) {
	inputs := []string{
		"acme",
		"acme.vpc.qoder.com.cn",
		"acme-gateway.vpc.qoder.com.cn",
		"acme-openapi.vpc.qoder.com.cn",
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Qoder.VPCEndpoint = input
			endpoints, err := ResolveEndpoints(cfg)
			if err != nil {
				t.Fatalf("ResolveEndpoints() error = %v", err)
			}
			if endpoints.WebBase != "https://acme.vpc.qoder.com.cn" {
				t.Errorf("WebBase = %q", endpoints.WebBase)
			}
			if endpoints.OpenAPIBase != "https://acme-openapi.vpc.qoder.com.cn" {
				t.Errorf("OpenAPIBase = %q", endpoints.OpenAPIBase)
			}
			if endpoints.InferBase != "https://acme-gateway.vpc.qoder.com.cn" {
				t.Errorf("InferBase = %q", endpoints.InferBase)
			}
			if endpoints.CenterBase != endpoints.InferBase {
				t.Errorf("CenterBase = %q, want %q", endpoints.CenterBase, endpoints.InferBase)
			}
		})
	}
}

func TestResolveEndpointsRejectsInvalidVPC(t *testing.T) {
	inputs := []string{
		"https://acme.vpc.qoder.com.cn",
		"acme.vpc.qoder.com.cn/path",
		"acme.example.com",
		"-acme",
		"acme-",
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Qoder.VPCEndpoint = input
			if _, err := ResolveEndpoints(cfg); err == nil {
				t.Fatalf("ResolveEndpoints() accepted invalid value %q", input)
			}
		})
	}
}

func TestQoderVPCDeviceFlowUsesDedicatedDomain(t *testing.T) {
	cfg := &config.Config{}
	cfg.Qoder.VPCEndpoint = "acme.vpc.qoder.com.cn"
	auth := NewQoderAuth(cfg)
	flow, err := auth.InitiateDeviceFlow(context.Background())
	if err != nil {
		t.Fatalf("InitiateDeviceFlow() error = %v", err)
	}
	verificationURL, err := url.Parse(flow.VerificationURIComplete)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if verificationURL.Host != "acme.vpc.qoder.com.cn" {
		t.Fatalf("verification host = %q", verificationURL.Host)
	}
	if verificationURL.Query().Get("client_id") != qoderCLIClientID {
		t.Fatalf("client_id = %q", verificationURL.Query().Get("client_id"))
	}
}
