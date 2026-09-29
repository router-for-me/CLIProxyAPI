package ionet

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/samber/lo"
)

// GetAvailableReplicas retrieves available replicas per location for specified hardware
func (c *Client) GetAvailableReplicas(hardwareID int, gpuCount int) (*AvailableReplicasResponse, error) {
	if hardwareID <= 0 {
		return nil, fmt.Errorf("hardware_id must be greater than 0")
	}
	if gpuCount < 1 {
		return nil, fmt.Errorf("gpu_count must be at least 1")
	}

	params := map[string]interface{}{
		"hardware_id":  hardwareID,
		"hardware_qty": gpuCount,
	}

	endpoint := "/available-replicas" + buildQueryParams(params)

	resp, err := c.makeRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get available replicas: %w", err)
	}

	type availableReplicaPayload struct {
		ID                int    `json:"id"`
		ISO2              string `json:"iso2"`
		Name              string `json:"name"`
		AvailableReplicas int    `json:"available_replicas"`
	}
	var payload []availableReplicaPayload

	if err := decodeData(resp.Body, &payload); err != nil {
		return nil, fmt.Errorf("failed to parse available replicas response: %w", err)
	}

	replicas := lo.Map(payload, func(item availableReplicaPayload, _ int) AvailableReplica {
		return AvailableReplica{
			LocationID:     item.ID,
			LocationName:   item.Name,
			HardwareID:     hardwareID,
			HardwareName:   "",
			AvailableCount: item.AvailableReplicas,
			MaxGPUs:        gpuCount,
		}
	})

	return &AvailableReplicasResponse{Replicas: replicas}, nil
}

// GetMaxGPUsPerContainer retrieves the maximum number of GPUs available per hardware type
func (c *Client) GetMaxGPUsPerContainer() (*MaxGPUResponse, error) {
	resp, err := c.makeRequest("GET", "/hardware/max-gpus-per-container", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get max GPUs per container: %w", err)
	}

	var maxGPUResp MaxGPUResponse
	if err := decodeData(resp.Body, &maxGPUResp); err != nil {
		return nil, fmt.Errorf("failed to parse max GPU response: %w", err)
	}

	return &maxGPUResp, nil
}

// ListHardwareTypes retrieves available hardware types using the max GPUs endpoint
func (c *Client) ListHardwareTypes() ([]HardwareType, int, error) {
	maxGPUResp, err := c.GetMaxGPUsPerContainer()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list hardware types: %w", err)
	}

	mapped := lo.Map(maxGPUResp.Hardware, func(hw MaxGPUInfo, _ int) HardwareType {
		name := strings.TrimSpace(hw.HardwareName)
		if name == "" {
			name = fmt.Sprintf("Hardware %d", hw.HardwareID)
		}

		return HardwareType{
			ID:             hw.HardwareID,
			Name:           name,
			GPUType:        "",
			GPUMemory:      0,
			MaxGPUs:        hw.MaxGPUsPerContainer,
			CPU:            "",
			Memory:         0,
			Storage:        0,
			HourlyRate:     0,
			Available:      hw.Available > 0,
			BrandName:      strings.TrimSpace(hw.BrandName),
			AvailableCount: hw.Available,
		}
	})

	totalAvailable := maxGPUResp.Total
	if totalAvailable == 0 {
		totalAvailable = lo.SumBy(maxGPUResp.Hardware, func(hw MaxGPUInfo) int {
			return hw.Available
		})
	}

	return mapped, totalAvailable, nil
}

// ListLocations retrieves available deployment locations (if supported by the API)
func (c *Client) ListLocations() (*LocationsResponse, error) {
	resp, err := c.makeRequest("GET", "/locations", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list locations: %w", err)
	}

	var locations LocationsResponse
	if err := decodeData(resp.Body, &locations); err != nil {
		return nil, fmt.Errorf("failed to parse locations response: %w", err)
	}

	locations.Locations = lo.Map(locations.Locations, func(location Location, _ int) Location {
		location.ISO2 = strings.ToUpper(strings.TrimSpace(location.ISO2))
		return location
	})

	if locations.Total == 0 {
		locations.Total = lo.SumBy(locations.Locations, func(location Location) int {
			return location.Available
		})
	}

	return &locations, nil
}

// GetHardwareType retrieves details about a specific hardware type
func (c *Client) GetHardwareType(hardwareID int) (*HardwareType, error) {
	if hardwareID <= 0 {
		return nil, fmt.Errorf("hardware ID must be greater than 0")
	}

	endpoint := fmt.Sprintf("/hardware/types/%d", hardwareID)

	resp, err := c.makeRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get hardware type: %w", err)
	}

	// API response format not documented, assuming direct format
	var hardwareType HardwareType
	if err := json.Unmarshal(resp.Body, &hardwareType); err != nil {
		return nil, fmt.Errorf("failed to parse hardware type: %w", err)
	}

	return &hardwareType, nil
}

// GetLocation retrieves details about a specific location
func (c *Client) GetLocation(locationID int) (*Location, error) {
	if locationID <= 0 {
		return nil, fmt.Errorf("location ID must be greater than 0")
	}

	endpoint := fmt.Sprintf("/locations/%d", locationID)

	resp, err := c.makeRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get location: %w", err)
	}

	// API response format not documented, assuming direct format
	var location Location
	if err := json.Unmarshal(resp.Body, &location); err != nil {
		return nil, fmt.Errorf("failed to parse location: %w", err)
	}

	return &location, nil
}

// GetLocationAvailability retrieves real-time availability for a specific location
func (c *Client) GetLocationAvailability(locationID int) (*LocationAvailability, error) {
	if locationID <= 0 {
		return nil, fmt.Errorf("location ID must be greater than 0")
	}

	endpoint := fmt.Sprintf("/locations/%d/availability", locationID)

	resp, err := c.makeRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get location availability: %w", err)
	}

	// API response format not documented, assuming direct format
	var availability LocationAvailability
	if err := json.Unmarshal(resp.Body, &availability); err != nil {
		return nil, fmt.Errorf("failed to parse location availability: %w", err)
	}

	return &availability, nil
}
