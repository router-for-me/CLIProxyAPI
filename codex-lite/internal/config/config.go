package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	OAuth    OAuthConfig    `yaml:"oauth"`
	AuthDir  string         `yaml:"auth_dir"`
	Selector SelectorConfig `yaml:"selector"`
	Proxy    ProxyConfig    `yaml:"proxy"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type OAuthConfig struct {
	CallbackPort int `yaml:"callback_port"`
}

type SelectorConfig struct {
	Type string `yaml:"type"`
}

type ProxyConfig struct {
	Timeout time.Duration `yaml:"timeout"`
	Retry   int           `yaml:"retry"`
}

func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 8080,
		},
		OAuth: OAuthConfig{
			CallbackPort: 1455,
		},
		AuthDir: "./data/auth",
		Selector: SelectorConfig{
			Type: "round_robin",
		},
		Proxy: ProxyConfig{
			Timeout: 120 * time.Second,
			Retry:   3,
		},
	}
}

func Load(path string) (*Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
