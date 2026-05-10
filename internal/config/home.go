package config

// HomeConfig configures the optional "home" control plane integration over Redis protocol.
type HomeConfig struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Host     string `yaml:"host" json:"-"`
	Port     int    `yaml:"port" json:"-"`
	Password string `yaml:"password" json:"-"`
}
