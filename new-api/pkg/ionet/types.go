package ionet

import (
	"time"
)

// Client represents the IO.NET API client
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient HTTPClient
}

// HTTPClient interface for making HTTP requests
type HTTPClient interface {
	Do(req *HTTPRequest) (*HTTPResponse, error)
}

// HTTPRequest represents an HTTP request
type HTTPRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// HTTPResponse represents an HTTP response
type HTTPResponse struct {
	StatusCode int
	Headers    map[string]string
	Body       []byte
}

// DeploymentRequest represents a container deployment request
type DeploymentRequest struct {
	ResourcePrivateName string          `json:"resource_private_name"`
	DurationHours       int             `json:"duration_hours"`
	GPUsPerContainer    int             `json:"gpus_per_container"`
	HardwareID          int             `json:"hardware_id"`
	LocationIDs         []int           `json:"location_ids"`
	ContainerConfig     ContainerConfig `json:"container_config"`
	RegistryConfig      RegistryConfig  `json:"registry_config"`
}

// ContainerConfig represents container configuration
type ContainerConfig struct {
	ReplicaCount       int               `json:"replica_count"`
	EnvVariables       map[string]string `json:"env_variables,omitempty"`
	SecretEnvVariables map[string]string `json:"secret_env_variables,omitempty"`
	Entrypoint         []string          `json:"entrypoint,omitempty"`
	TrafficPort        int               `json:"traffic_port,omitempty"`
	Args               []string          `json:"args,omitempty"`
}

// RegistryConfig represents registry configuration
type RegistryConfig struct {
	ImageURL         string `json:"image_url"`
	RegistryUsername string `json:"registry_username,omitempty"`
	RegistrySecret   string `json:"registry_secret,omitempty"`
}

// DeploymentResponse represents the response from deployment creation
type DeploymentResponse struct {
	DeploymentID string `json:"deployment_id"`
	Status       string `json:"status"`
}

// DeploymentDetail represents detailed deployment information
type DeploymentDetail struct {
	ID                      string                    `json:"id"`
	Status                  string                    `json:"status"`
	CreatedAt               time.Time                 `json:"created_at"`
	StartedAt               *time.Time                `json:"started_at,omitempty"`
	FinishedAt              *time.Time                `json:"finished_at,omitempty"`
	AmountPaid              float64                   `json:"amount_paid"`
	CompletedPercent        float64                   `json:"completed_percent"`
	TotalGPUs               int                       `json:"total_gpus"`
	GPUsPerContainer        int                       `json:"gpus_per_container"`
	TotalContainers         int                       `json:"total_containers"`
	HardwareName            string                    `json:"hardware_name"`
	HardwareID              int                       `json:"hardware_id"`
	Locations               []DeploymentLocation      `json:"locations"`
	BrandName               string                    `json:"brand_name"`
	ComputeMinutesServed    int                       `json:"compute_minutes_served"`
	ComputeMinutesRemaining int                       `json:"compute_minutes_remaining"`
	ContainerConfig         DeploymentContainerConfig `json:"container_config"`
}

// DeploymentLocation represents a location in deployment details
type DeploymentLocation struct {
	ID   int    `json:"id"`
	ISO2 string `json:"iso2"`
	Name string `json:"name"`
}

// DeploymentContainerConfig represents container config in deployment details
type DeploymentContainerConfig struct {
	Entrypoint   []string               `json:"entrypoint"`
	EnvVariables map[string]interface{} `json:"env_variables"`
	TrafficPort  int                    `json:"traffic_port"`
	ImageURL     string                 `json:"image_url"`
}

// Container represents a container within a deployment
type Container struct {
	DeviceID         string           `json:"device_id"`
	ContainerID      string           `json:"container_id"`
	Hardware         string           `json:"hardware"`
	BrandName        string           `json:"brand_name"`
	CreatedAt        time.Time        `json:"created_at"`
	UptimePercent    int              `json:"uptime_percent"`
	GPUsPerContainer int              `json:"gpus_per_container"`
	Status           string           `json:"status"`
	ContainerEvents  []ContainerEvent `json:"container_events"`
	PublicURL        string           `json:"public_url"`
}

// ContainerEvent represents a container event
type ContainerEvent struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

// ContainerList represents a list of containers
type ContainerList struct {
	Total   int         `json:"total"`
	Workers []Container `json:"workers"`
}

// Deployment represents a deployment in the list
type Deployment struct {
	ID                      string    `json:"id"`
	Status                  string    `json:"status"`
	Name                    string    `json:"name"`
	CompletedPercent        float64   `json:"completed_percent"`
	HardwareQuantity        int       `json:"hardware_quantity"`
	BrandName               string    `json:"brand_name"`
	HardwareName            string    `json:"hardware_name"`
	Served                  string    `json:"served"`
	Remaining               string    `json:"remaining"`
	ComputeMinutesServed    int       `json:"compute_minutes_served"`
	ComputeMinutesRemaining int       `json:"compute_minutes_remaining"`
	CreatedAt               time.Time `json:"created_at"`
	GPUCount                int       `json:"-"` // Derived from HardwareQuantity
	Replicas                int       `json:"-"` // Derived from HardwareQuantity
}

// DeploymentList represents a list of deployments with pagination
type DeploymentList struct {
	Deployments []Deployment `json:"deployments"`
	Total       int          `json:"total"`
	Statuses    []string     `json:"statuses"`
}

// AvailableReplica represents replica availability for a location
type AvailableReplica struct {
	LocationID     int    `json:"location_id"`
	LocationName   string `json:"location_name"`
	HardwareID     int    `json:"hardware_id"`
	HardwareName   string `json:"hardware_name"`
	AvailableCount int    `json:"available_count"`
	MaxGPUs        int    `json:"max_gpus"`
}

// AvailableReplicasResponse represents the response for available replicas
type AvailableReplicasResponse struct {
	Replicas []AvailableReplica `json:"replicas"`
}

// MaxGPUResponse represents the response for maximum GPUs per container
type MaxGPUResponse struct {
	Hardware []MaxGPUInfo `json:"hardware"`
	Total    int          `json:"total"`
}

// MaxGPUInfo represents max GPU information for a hardware type
type MaxGPUInfo struct {
	MaxGPUsPerContainer int    `json:"max_gpus_per_container"`
	Available           int    `json:"available"`
	HardwareID          int    `json:"hardware_id"`
	HardwareName        string `json:"hardware_name"`
	BrandName           string `json:"brand_name"`
}

// PriceEstimationRequest represents a price estimation request
type PriceEstimationRequest struct {
	LocationIDs      []int  `json:"location_ids"`
	HardwareID       int    `json:"hardware_id"`
	GPUsPerContainer int    `json:"gpus_per_container"`
	DurationHours    int    `json:"duration_hours"`
	ReplicaCount     int    `json:"replica_count"`
	Currency         string `json:"currency"`
	DurationType     string `json:"duration_type"`
	DurationQty      int    `json:"duration_qty"`
	HardwareQty      int    `json:"hardware_qty"`
}

// PriceEstimationResponse represents the price estimation response
type PriceEstimationResponse struct {
	EstimatedCost   float64        `json:"estimated_cost"`
	Currency        string         `json:"currency"`
	PriceBreakdown  PriceBreakdown `json:"price_breakdown"`
	EstimationValid bool           `json:"estimation_valid"`
}

// PriceBreakdown represents detailed cost breakdown
type PriceBreakdown struct {
	ComputeCost float64 `json:"compute_cost"`
	NetworkCost float64 `json:"network_cost,omitempty"`
	StorageCost float64 `json:"storage_cost,omitempty"`
	TotalCost   float64 `json:"total_cost"`
	HourlyRate  float64 `json:"hourly_rate"`
}

// ContainerLogs represents container log entries
type ContainerLogs struct {
	ContainerID string     `json:"container_id"`
	Logs        []LogEntry `json:"logs"`
	HasMore     bool       `json:"has_more"`
	NextCursor  string     `json:"next_cursor,omitempty"`
}

// LogEntry represents a single log entry
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level,omitempty"`
	Message   string    `json:"message"`
	Source    string    `json:"source,omitempty"`
}

// UpdateDeploymentRequest represents request to update deployment configuration
type UpdateDeploymentRequest struct {
	EnvVariables       map[string]string `json:"env_variables,omitempty"`
	SecretEnvVariables map[string]string `json:"secret_env_variables,omitempty"`
	Entrypoint         []string          `json:"entrypoint,omitempty"`
	TrafficPort        *int              `json:"traffic_port,omitempty"`
	ImageURL           string            `json:"image_url,omitempty"`
	RegistryUsername   string            `json:"registry_username,omitempty"`
	RegistrySecret     string            `json:"registry_secret,omitempty"`
	Args               []string          `json:"args,omitempty"`
	Command            string            `json:"command,omitempty"`
}

// ExtendDurationRequest represents request to extend deployment duration
type ExtendDurationRequest struct {
	DurationHours int `json:"duration_hours"`
}

// UpdateDeploymentResponse represents response from deployment update
type UpdateDeploymentResponse struct {
	Status       string `json:"status"`
	DeploymentID string `json:"deployment_id"`
}

// UpdateClusterNameRequest represents request to update cluster name
type UpdateClusterNameRequest struct {
	Name string `json:"cluster_name"`
}

// UpdateClusterNameResponse represents response from cluster name update
type UpdateClusterNameResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// APIError represents an API error response
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// Error implements the error interface
func (e *APIError) Error() string {
	if e.Details != "" {
		return e.Message + ": " + e.Details
	}
	return e.Message
}

// ListDeploymentsOptions represents options for listing deployments
type ListDeploymentsOptions struct {
	Status     string `json:"status,omitempty"`      // filter by status
	LocationID int    `json:"location_id,omitempty"` // filter by location
	Page       int    `json:"page,omitempty"`        // pagination
	PageSize   int    `json:"page_size,omitempty"`   // pagination
	SortBy     string `json:"sort_by,omitempty"`     // sort field
	SortOrder  string `json:"sort_order,omitempty"`  // asc/desc
}

// GetLogsOptions represents options for retrieving container logs
type GetLogsOptions struct {
	StartTime *time.Time `json:"start_time,omitempty"`
	EndTime   *time.Time `json:"end_time,omitempty"`
	Level     string     `json:"level,omitempty"`  // filter by log level
	Stream    string     `json:"stream,omitempty"` // filter by stdout/stderr streams
	Limit     int        `json:"limit,omitempty"`  // max number of log entries
	Cursor    string     `json:"cursor,omitempty"` // pagination cursor
	Follow    bool       `json:"follow,omitempty"` // stream logs
}

// HardwareType represents a hardware type available for deployment
type HardwareType struct {
	ID             int     `json:"id"`
	Name           string  `json:"name"`
	Description    string  `json:"description,omitempty"`
	GPUType        string  `json:"gpu_type"`
	GPUMemory      int     `json:"gpu_memory"` // in GB
	MaxGPUs        int     `json:"max_gpus"`
	CPU            string  `json:"cpu,omitempty"`
	Memory         int     `json:"memory,omitempty"`  // in GB
	Storage        int     `json:"storage,omitempty"` // in GB
	HourlyRate     float64 `json:"hourly_rate"`
	Available      bool    `json:"available"`
	BrandName      string  `json:"brand_name,omitempty"`
	AvailableCount int     `json:"available_count,omitempty"`
}

// Location represents a deployment location
type Location struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	ISO2        string  `json:"iso2,omitempty"`
	Region      string  `json:"region,omitempty"`
	Country     string  `json:"country,omitempty"`
	Latitude    float64 `json:"latitude,omitempty"`
	Longitude   float64 `json:"longitude,omitempty"`
	Available   int     `json:"available,omitempty"`
	Description string  `json:"description,omitempty"`
}

// LocationsResponse represents the list of locations and aggregated metadata.
type LocationsResponse struct {
	Locations []Location `json:"locations"`
	Total     int        `json:"total"`
}

// LocationAvailability represents real-time availability for a location
type LocationAvailability struct {
	LocationID           int                    `json:"location_id"`
	LocationName         string                 `json:"location_name"`
	Available            bool                   `json:"available"`
	HardwareAvailability []HardwareAvailability `json:"hardware_availability"`
	UpdatedAt            time.Time              `json:"updated_at"`
}

// HardwareAvailability represents availability for specific hardware at a location
type HardwareAvailability struct {
	HardwareID     int    `json:"hardware_id"`
	HardwareName   string `json:"hardware_name"`
	AvailableCount int    `json:"available_count"`
	MaxGPUs        int    `json:"max_gpus"`
}
