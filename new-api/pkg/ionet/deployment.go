package ionet

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/samber/lo"
)

// DeployContainer deploys a new container with the specified configuration
func (c *Client) DeployContainer(req *DeploymentRequest) (*DeploymentResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("deployment request cannot be nil")
	}

	// Validate required fields
	if req.ResourcePrivateName == "" {
		return nil, fmt.Errorf("resource_private_name is required")
	}
	if len(req.LocationIDs) == 0 {
		return nil, fmt.Errorf("location_ids is required")
	}
	if req.HardwareID <= 0 {
		return nil, fmt.Errorf("hardware_id is required")
	}
	if req.RegistryConfig.ImageURL == "" {
		return nil, fmt.Errorf("registry_config.image_url is required")
	}
	if req.GPUsPerContainer < 1 {
		return nil, fmt.Errorf("gpus_per_container must be at least 1")
	}
	if req.DurationHours < 1 {
		return nil, fmt.Errorf("duration_hours must be at least 1")
	}
	if req.ContainerConfig.ReplicaCount < 1 {
		return nil, fmt.Errorf("container_config.replica_count must be at least 1")
	}

	resp, err := c.makeRequest("POST", "/deploy", req)
	if err != nil {
		return nil, fmt.Errorf("failed to deploy container: %w", err)
	}

	// API returns direct format:
	// {"status": "string", "deployment_id": "..."}
	var deployResp DeploymentResponse
	if err := json.Unmarshal(resp.Body, &deployResp); err != nil {
		return nil, fmt.Errorf("failed to parse deployment response: %w", err)
	}

	return &deployResp, nil
}

// ListDeployments retrieves a list of deployments with optional filtering
func (c *Client) ListDeployments(opts *ListDeploymentsOptions) (*DeploymentList, error) {
	params := make(map[string]interface{})

	if opts != nil {
		params["status"] = opts.Status
		params["location_id"] = opts.LocationID
		params["page"] = opts.Page
		params["page_size"] = opts.PageSize
		params["sort_by"] = opts.SortBy
		params["sort_order"] = opts.SortOrder
	}

	endpoint := "/deployments" + buildQueryParams(params)

	resp, err := c.makeRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list deployments: %w", err)
	}

	var deploymentList DeploymentList
	if err := decodeData(resp.Body, &deploymentList); err != nil {
		return nil, fmt.Errorf("failed to parse deployments list: %w", err)
	}

	deploymentList.Deployments = lo.Map(deploymentList.Deployments, func(deployment Deployment, _ int) Deployment {
		deployment.GPUCount = deployment.HardwareQuantity
		deployment.Replicas = deployment.HardwareQuantity // Assuming 1:1 mapping for now
		return deployment
	})

	return &deploymentList, nil
}

// GetDeployment retrieves detailed information about a specific deployment
func (c *Client) GetDeployment(deploymentID string) (*DeploymentDetail, error) {
	if deploymentID == "" {
		return nil, fmt.Errorf("deployment ID cannot be empty")
	}

	endpoint := fmt.Sprintf("/deployment/%s", deploymentID)

	resp, err := c.makeRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment details: %w", err)
	}

	var deploymentDetail DeploymentDetail
	if err := decodeDataWithFlexibleTimes(resp.Body, &deploymentDetail); err != nil {
		return nil, fmt.Errorf("failed to parse deployment details: %w", err)
	}

	return &deploymentDetail, nil
}

// UpdateDeployment updates the configuration of an existing deployment
func (c *Client) UpdateDeployment(deploymentID string, req *UpdateDeploymentRequest) (*UpdateDeploymentResponse, error) {
	if deploymentID == "" {
		return nil, fmt.Errorf("deployment ID cannot be empty")
	}
	if req == nil {
		return nil, fmt.Errorf("update request cannot be nil")
	}

	endpoint := fmt.Sprintf("/deployment/%s", deploymentID)

	resp, err := c.makeRequest("PATCH", endpoint, req)
	if err != nil {
		return nil, fmt.Errorf("failed to update deployment: %w", err)
	}

	// API returns direct format:
	// {"status": "string", "deployment_id": "..."}
	var updateResp UpdateDeploymentResponse
	if err := json.Unmarshal(resp.Body, &updateResp); err != nil {
		return nil, fmt.Errorf("failed to parse update deployment response: %w", err)
	}

	return &updateResp, nil
}

// ExtendDeployment extends the duration of an existing deployment
func (c *Client) ExtendDeployment(deploymentID string, req *ExtendDurationRequest) (*DeploymentDetail, error) {
	if deploymentID == "" {
		return nil, fmt.Errorf("deployment ID cannot be empty")
	}
	if req == nil {
		return nil, fmt.Errorf("extend request cannot be nil")
	}
	if req.DurationHours < 1 {
		return nil, fmt.Errorf("duration_hours must be at least 1")
	}

	endpoint := fmt.Sprintf("/deployment/%s/extend", deploymentID)

	resp, err := c.makeRequest("POST", endpoint, req)
	if err != nil {
		return nil, fmt.Errorf("failed to extend deployment: %w", err)
	}

	var deploymentDetail DeploymentDetail
	if err := decodeDataWithFlexibleTimes(resp.Body, &deploymentDetail); err != nil {
		return nil, fmt.Errorf("failed to parse extended deployment details: %w", err)
	}

	return &deploymentDetail, nil
}

// DeleteDeployment deletes an active deployment
func (c *Client) DeleteDeployment(deploymentID string) (*UpdateDeploymentResponse, error) {
	if deploymentID == "" {
		return nil, fmt.Errorf("deployment ID cannot be empty")
	}

	endpoint := fmt.Sprintf("/deployment/%s", deploymentID)

	resp, err := c.makeRequest("DELETE", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to delete deployment: %w", err)
	}

	// API returns direct format:
	// {"status": "string", "deployment_id": "..."}
	var deleteResp UpdateDeploymentResponse
	if err := json.Unmarshal(resp.Body, &deleteResp); err != nil {
		return nil, fmt.Errorf("failed to parse delete deployment response: %w", err)
	}

	return &deleteResp, nil
}

// GetPriceEstimation calculates the estimated cost for a deployment
func (c *Client) GetPriceEstimation(req *PriceEstimationRequest) (*PriceEstimationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("price estimation request cannot be nil")
	}

	// Validate required fields
	if len(req.LocationIDs) == 0 {
		return nil, fmt.Errorf("location_ids is required")
	}
	if req.HardwareID == 0 {
		return nil, fmt.Errorf("hardware_id is required")
	}
	if req.ReplicaCount < 1 {
		return nil, fmt.Errorf("replica_count must be at least 1")
	}

	currency := strings.TrimSpace(req.Currency)
	if currency == "" {
		currency = "usdc"
	}

	durationType := strings.TrimSpace(req.DurationType)
	if durationType == "" {
		durationType = "hour"
	}
	durationType = strings.ToLower(durationType)

	apiDurationType := ""

	durationQty := req.DurationQty
	if durationQty < 1 {
		durationQty = req.DurationHours
	}
	if durationQty < 1 {
		return nil, fmt.Errorf("duration_qty must be at least 1")
	}

	hardwareQty := req.HardwareQty
	if hardwareQty < 1 {
		hardwareQty = req.GPUsPerContainer
	}
	if hardwareQty < 1 {
		return nil, fmt.Errorf("hardware_qty must be at least 1")
	}

	durationHoursForRate := req.DurationHours
	if durationHoursForRate < 1 {
		durationHoursForRate = durationQty
	}
	switch durationType {
	case "hour", "hours", "hourly":
		durationHoursForRate = durationQty
		apiDurationType = "hourly"
	case "day", "days", "daily":
		durationHoursForRate = durationQty * 24
		apiDurationType = "daily"
	case "week", "weeks", "weekly":
		durationHoursForRate = durationQty * 24 * 7
		apiDurationType = "weekly"
	case "month", "months", "monthly":
		durationHoursForRate = durationQty * 24 * 30
		apiDurationType = "monthly"
	}
	if durationHoursForRate < 1 {
		durationHoursForRate = 1
	}
	if apiDurationType == "" {
		apiDurationType = "hourly"
	}

	params := map[string]interface{}{
		"location_ids":       req.LocationIDs,
		"hardware_id":        req.HardwareID,
		"hardware_qty":       hardwareQty,
		"gpus_per_container": req.GPUsPerContainer,
		"duration_type":      apiDurationType,
		"duration_qty":       durationQty,
		"duration_hours":     req.DurationHours,
		"replica_count":      req.ReplicaCount,
		"currency":           currency,
	}

	endpoint := "/price" + buildQueryParams(params)

	resp, err := c.makeRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get price estimation: %w", err)
	}

	// Parse according to the actual API response format from docs:
	// {
	//   "data": {
	//     "replica_count": 0,
	//     "gpus_per_container": 0,
	//     "available_replica_count": [0],
	//     "discount": 0,
	//     "ionet_fee": 0,
	//     "ionet_fee_percent": 0,
	//     "currency_conversion_fee": 0,
	//     "currency_conversion_fee_percent": 0,
	//     "total_cost_usdc": 0
	//   }
	// }
	var pricingData struct {
		ReplicaCount                 int     `json:"replica_count"`
		GPUsPerContainer             int     `json:"gpus_per_container"`
		AvailableReplicaCount        []int   `json:"available_replica_count"`
		Discount                     float64 `json:"discount"`
		IonetFee                     float64 `json:"ionet_fee"`
		IonetFeePercent              float64 `json:"ionet_fee_percent"`
		CurrencyConversionFee        float64 `json:"currency_conversion_fee"`
		CurrencyConversionFeePercent float64 `json:"currency_conversion_fee_percent"`
		TotalCostUSDC                float64 `json:"total_cost_usdc"`
	}

	if err := decodeData(resp.Body, &pricingData); err != nil {
		return nil, fmt.Errorf("failed to parse price estimation response: %w", err)
	}

	// Convert to our internal format
	durationHoursFloat := float64(durationHoursForRate)
	if durationHoursFloat <= 0 {
		durationHoursFloat = 1
	}

	priceResp := &PriceEstimationResponse{
		EstimatedCost:   pricingData.TotalCostUSDC,
		Currency:        strings.ToUpper(currency),
		EstimationValid: true,
		PriceBreakdown: PriceBreakdown{
			ComputeCost: pricingData.TotalCostUSDC - pricingData.IonetFee - pricingData.CurrencyConversionFee,
			TotalCost:   pricingData.TotalCostUSDC,
			HourlyRate:  pricingData.TotalCostUSDC / durationHoursFloat,
		},
	}

	return priceResp, nil
}

// CheckClusterNameAvailability checks if a cluster name is available
func (c *Client) CheckClusterNameAvailability(clusterName string) (bool, error) {
	if clusterName == "" {
		return false, fmt.Errorf("cluster name cannot be empty")
	}

	params := map[string]interface{}{
		"cluster_name": clusterName,
	}

	endpoint := "/clusters/check_cluster_name_availability" + buildQueryParams(params)

	resp, err := c.makeRequest("GET", endpoint, nil)
	if err != nil {
		return false, fmt.Errorf("failed to check cluster name availability: %w", err)
	}

	var availabilityResp bool
	if err := json.Unmarshal(resp.Body, &availabilityResp); err != nil {
		return false, fmt.Errorf("failed to parse cluster name availability response: %w", err)
	}

	return availabilityResp, nil
}

// UpdateClusterName updates the name of an existing cluster/deployment
func (c *Client) UpdateClusterName(clusterID string, req *UpdateClusterNameRequest) (*UpdateClusterNameResponse, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("cluster ID cannot be empty")
	}
	if req == nil {
		return nil, fmt.Errorf("update cluster name request cannot be nil")
	}
	if req.Name == "" {
		return nil, fmt.Errorf("cluster name cannot be empty")
	}

	endpoint := fmt.Sprintf("/clusters/%s/update-name", clusterID)

	resp, err := c.makeRequest("PUT", endpoint, req)
	if err != nil {
		return nil, fmt.Errorf("failed to update cluster name: %w", err)
	}

	// Parse the response directly without data wrapper based on API docs
	var updateResp UpdateClusterNameResponse
	if err := json.Unmarshal(resp.Body, &updateResp); err != nil {
		return nil, fmt.Errorf("failed to parse update cluster name response: %w", err)
	}

	return &updateResp, nil
}
