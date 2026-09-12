package config

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCodexLiveMediaRelayConfigParsesAndValidates(t *testing.T) {
	var cfg Config
	raw := []byte(`codex:
  live-media-relay:
    enabled: true
    max-sessions: 64
    disable-private-remote-ips: true
    public-ip: "203.0.113.10"
    udp-port-min: 40000
    udp-port-max: 40150
    ice-servers:
      - urls: ["stun:stun.example.com:3478"]
      - urls: ["turn:turn.example.com:3478?transport=udp"]
        username: "relay-user"
        credential: "relay-secret"
`)
	if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
		t.Fatalf("unmarshal Codex Live media relay config: %v", errUnmarshal)
	}
	relay := cfg.Codex.LiveMediaRelay
	if !relay.Enabled || relay.MaxSessions != 64 || !relay.DisablePrivateRemoteIPs || relay.PublicIP != "203.0.113.10" {
		t.Fatalf("parsed media relay = %#v", relay)
	}
	if relay.UDPPortMin != 40000 || relay.UDPPortMax != 40150 {
		t.Fatalf("parsed UDP range = %d-%d", relay.UDPPortMin, relay.UDPPortMax)
	}
	if len(relay.ICEServers) != 2 || relay.ICEServers[1].Credential != "relay-secret" {
		t.Fatalf("parsed ICE servers = %#v", relay.ICEServers)
	}
	if errValidate := relay.Validate(); errValidate != nil {
		t.Fatalf("Validate() error = %v", errValidate)
	}
	encoded, errMarshal := json.Marshal(relay)
	if errMarshal != nil {
		t.Fatalf("marshal media relay config: %v", errMarshal)
	}
	for _, sensitive := range []string{"relay-secret", "credential", "relay-user", "username"} {
		if strings.Contains(string(encoded), sensitive) {
			t.Fatalf("JSON media relay config leaked TURN field %q: %s", sensitive, encoded)
		}
	}
}

func TestCodexLiveMediaRelayConfigMigratesLegacyPrivateIPSetting(t *testing.T) {
	for name, raw := range map[string]string{
		"legacy allow true":  "allow-private-remote-ips: true\n",
		"legacy allow false": "allow-private-remote-ips: false\n",
		"new default":        "enabled: true\n",
	} {
		t.Run(name, func(t *testing.T) {
			var relay CodexLiveMediaRelayConfig
			if errUnmarshal := yaml.Unmarshal([]byte(raw), &relay); errUnmarshal != nil {
				t.Fatalf("unmarshal media relay config: %v", errUnmarshal)
			}
			wantDisabled := name == "legacy allow false"
			if relay.DisablePrivateRemoteIPs != wantDisabled {
				t.Fatalf("disable-private-remote-ips = %t, want %t", relay.DisablePrivateRemoteIPs, wantDisabled)
			}
		})
	}

	var relay CodexLiveMediaRelayConfig
	errUnmarshal := yaml.Unmarshal([]byte("allow-private-remote-ips: true\ndisable-private-remote-ips: false\n"), &relay)
	if errUnmarshal == nil {
		t.Fatal("accepted conflicting private IP settings")
	}
}

func TestCodexLiveMediaRelayConfigRejectsInvalidValues(t *testing.T) {
	for name, relay := range map[string]CodexLiveMediaRelayConfig{
		"negative session limit": {
			Enabled:     true,
			MaxSessions: -1,
		},
		"invalid public IP": {
			Enabled:  true,
			PublicIP: "not-an-ip",
		},
		"partial UDP range": {
			Enabled:    true,
			UDPPortMin: 40000,
		},
		"reversed UDP range": {
			Enabled:    true,
			UDPPortMin: 40100,
			UDPPortMax: 40000,
		},
		"undersized UDP range": {
			Enabled:     true,
			MaxSessions: 2,
			UDPPortMin:  40000,
			UDPPortMax:  40002,
		},
		"missing ICE URLs": {
			Enabled:    true,
			ICEServers: []CodexLiveICEServer{{Username: "user"}},
		},
		"unsupported ICE URL": {
			Enabled:    true,
			ICEServers: []CodexLiveICEServer{{URLs: []string{"https://example.com"}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if errValidate := relay.Validate(); errValidate == nil {
				t.Fatal("Validate() accepted invalid media relay config")
			}
		})
	}
}
