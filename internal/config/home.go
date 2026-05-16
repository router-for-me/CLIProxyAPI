package config

// HomeConfig configures the optional "home" control plane integration over Redis protocol.
type HomeConfig struct {
	Enabled  bool          `yaml:"enabled" json:"enabled"`
	Host     string        `yaml:"host" json:"-"`
	Port     int           `yaml:"port" json:"-"`
	Password string        `yaml:"password" json:"-"`
	TLS      HomeTLSConfig `yaml:"tls" json:"-"`
}

// HomeTLSConfig configures client-side TLS for the home Redis connection.
type HomeTLSConfig struct {
	Enable             bool   `yaml:"enable" json:"-"`
	ServerName         string `yaml:"server-name" json:"-"`
	InsecureSkipVerify bool   `yaml:"insecure-skip-verify" json:"-"`
	CACert             string `yaml:"ca-cert" json:"-"`
}
