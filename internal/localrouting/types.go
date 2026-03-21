package localrouting

import "time"

const (
	DefaultEdgePort = 1355
	MinAppPort      = 4000
	MaxAppPort      = 4999
)

// Config controls named local routing behavior.
type Config struct {
	Enabled      bool
	Name         string
	TLD          string
	EdgePort     int
	HTTPS        bool
	AppPort      int
	StateDir     string
	Force        bool
	DisplayOAuth bool
}

// RouteInfo describes one named route entry.
type RouteInfo struct {
	Name       string    `json:"name"`
	BaseName   string    `json:"base_name,omitempty"`
	Host       string    `json:"host"`
	TargetHost string    `json:"target_host"`
	TargetPort int       `json:"target_port"`
	PID        int       `json:"pid"`
	Command    string    `json:"command,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Status reports runtime local-routing state.
type Status struct {
	Enabled      bool      `json:"enabled"`
	URL          string    `json:"url,omitempty"`
	Name         string    `json:"name,omitempty"`
	Host         string    `json:"host,omitempty"`
	TLD          string    `json:"tld,omitempty"`
	EdgePort     int       `json:"edge_port,omitempty"`
	BackendHost  string    `json:"backend_host,omitempty"`
	BackendPort  int       `json:"backend_port,omitempty"`
	HTTPS        bool      `json:"https"`
	StateDir     string    `json:"state_dir,omitempty"`
	CACertPath   string    `json:"ca_cert_path,omitempty"`
	TrustCommand string    `json:"trust_command,omitempty"`
	EdgeOwnerPID int       `json:"edge_owner_pid,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}
