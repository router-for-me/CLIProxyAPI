package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/ionet"
	"github.com/gin-gonic/gin"
)

func getIoAPIKey(c *gin.Context) (string, bool) {
	common.OptionMapRWMutex.RLock()
	enabled := common.OptionMap["model_deployment.ionet.enabled"] == "true"
	apiKey := common.OptionMap["model_deployment.ionet.api_key"]
	common.OptionMapRWMutex.RUnlock()
	if !enabled || strings.TrimSpace(apiKey) == "" {
		common.ApiErrorMsg(c, "io.net model deployment is not enabled or api key missing")
		return "", false
	}
	return apiKey, true
}

func GetModelDeploymentSettings(c *gin.Context) {
	common.OptionMapRWMutex.RLock()
	enabled := common.OptionMap["model_deployment.ionet.enabled"] == "true"
	hasAPIKey := strings.TrimSpace(common.OptionMap["model_deployment.ionet.api_key"]) != ""
	common.OptionMapRWMutex.RUnlock()

	common.ApiSuccess(c, gin.H{
		"provider":    "io.net",
		"enabled":     enabled,
		"configured":  hasAPIKey,
		"can_connect": enabled && hasAPIKey,
	})
}

func getIoClient(c *gin.Context) (*ionet.Client, bool) {
	apiKey, ok := getIoAPIKey(c)
	if !ok {
		return nil, false
	}
	return ionet.NewClient(apiKey), true
}

func getIoEnterpriseClient(c *gin.Context) (*ionet.Client, bool) {
	apiKey, ok := getIoAPIKey(c)
	if !ok {
		return nil, false
	}
	return ionet.NewEnterpriseClient(apiKey), true
}

func TestIoNetConnection(c *gin.Context) {
	var req struct {
		APIKey string `json:"api_key"`
	}

	rawBody, err := c.GetRawData()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if len(bytes.TrimSpace(rawBody)) > 0 {
		if err := json.Unmarshal(rawBody, &req); err != nil {
			common.ApiErrorMsg(c, "invalid request payload")
			return
		}
	}

	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		common.OptionMapRWMutex.RLock()
		storedKey := strings.TrimSpace(common.OptionMap["model_deployment.ionet.api_key"])
		common.OptionMapRWMutex.RUnlock()
		if storedKey == "" {
			common.ApiErrorMsg(c, "api_key is required")
			return
		}
		apiKey = storedKey
	}

	client := ionet.NewEnterpriseClient(apiKey)
	result, err := client.GetMaxGPUsPerContainer()
	if err != nil {
		if apiErr, ok := err.(*ionet.APIError); ok {
			message := strings.TrimSpace(apiErr.Message)
			if message == "" {
				message = "failed to validate api key"
			}
			common.ApiErrorMsg(c, message)
			return
		}
		common.ApiError(c, err)
		return
	}

	totalHardware := 0
	totalAvailable := 0
	if result != nil {
		totalHardware = len(result.Hardware)
		totalAvailable = result.Total
		if totalAvailable == 0 {
			for _, hw := range result.Hardware {
				totalAvailable += hw.Available
			}
		}
	}

	common.ApiSuccess(c, gin.H{
		"hardware_count":  totalHardware,
		"total_available": totalAvailable,
	})
}

func requireDeploymentID(c *gin.Context) (string, bool) {
	deploymentID := strings.TrimSpace(c.Param("id"))
	if deploymentID == "" {
		common.ApiErrorMsg(c, "deployment ID is required")
		return "", false
	}
	return deploymentID, true
}

func requireContainerID(c *gin.Context) (string, bool) {
	containerID := strings.TrimSpace(c.Param("container_id"))
	if containerID == "" {
		common.ApiErrorMsg(c, "container ID is required")
		return "", false
	}
	return containerID, true
}

func mapIoNetDeployment(d ionet.Deployment) map[string]any {
	var created int64
	if d.CreatedAt.IsZero() {
		created = time.Now().Unix()
	} else {
		created = d.CreatedAt.Unix()
	}

	timeRemainingHours := d.ComputeMinutesRemaining / 60
	timeRemainingMins := d.ComputeMinutesRemaining % 60
	var timeRemaining string
	if timeRemainingHours > 0 {
		timeRemaining = fmt.Sprintf("%d hour %d minutes", timeRemainingHours, timeRemainingMins)
	} else if timeRemainingMins > 0 {
		timeRemaining = fmt.Sprintf("%d minutes", timeRemainingMins)
	} else {
		timeRemaining = "completed"
	}

	hardwareInfo := fmt.Sprintf("%s %s x%d", d.BrandName, d.HardwareName, d.HardwareQuantity)

	return map[string]any{
		"id":                        d.ID,
		"deployment_name":           d.Name,
		"container_name":            d.Name,
		"status":                    strings.ToLower(d.Status),
		"type":                      "Container",
		"time_remaining":            timeRemaining,
		"time_remaining_minutes":    d.ComputeMinutesRemaining,
		"hardware_info":             hardwareInfo,
		"hardware_name":             d.HardwareName,
		"brand_name":                d.BrandName,
		"hardware_quantity":         d.HardwareQuantity,
		"completed_percent":         d.CompletedPercent,
		"compute_minutes_served":    d.ComputeMinutesServed,
		"compute_minutes_remaining": d.ComputeMinutesRemaining,
		"created_at":                created,
		"updated_at":                created,
		"model_name":                "",
		"model_version":             "",
		"instance_count":            d.HardwareQuantity,
		"resource_config": map[string]any{
			"cpu":    "",
			"memory": "",
			"gpu":    strconv.Itoa(d.HardwareQuantity),
		},
		"description": "",
		"provider":    "io.net",
	}
}

func computeStatusCounts(total int, deployments []ionet.Deployment) map[string]int64 {
	counts := map[string]int64{
		"all": int64(total),
	}

	for _, status := range []string{"running", "completed", "failed", "deployment requested", "termination requested", "destroyed"} {
		counts[status] = 0
	}

	for _, d := range deployments {
		status := strings.ToLower(strings.TrimSpace(d.Status))
		counts[status] = counts[status] + 1
	}

	return counts
}

func GetAllDeployments(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	client, ok := getIoEnterpriseClient(c)
	if !ok {
		return
	}

	status := c.Query("status")
	opts := &ionet.ListDeploymentsOptions{
		Status:    strings.ToLower(strings.TrimSpace(status)),
		Page:      pageInfo.GetPage(),
		PageSize:  pageInfo.GetPageSize(),
		SortBy:    "created_at",
		SortOrder: "desc",
	}

	dl, err := client.ListDeployments(opts)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	items := make([]map[string]any, 0, len(dl.Deployments))
	for _, d := range dl.Deployments {
		items = append(items, mapIoNetDeployment(d))
	}

	data := gin.H{
		"page":          pageInfo.GetPage(),
		"page_size":     pageInfo.GetPageSize(),
		"total":         dl.Total,
		"items":         items,
		"status_counts": computeStatusCounts(dl.Total, dl.Deployments),
	}
	common.ApiSuccess(c, data)
}

func SearchDeployments(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	client, ok := getIoEnterpriseClient(c)
	if !ok {
		return
	}

	status := strings.ToLower(strings.TrimSpace(c.Query("status")))
	keyword := strings.TrimSpace(c.Query("keyword"))

	dl, err := client.ListDeployments(&ionet.ListDeploymentsOptions{
		Status:    status,
		Page:      pageInfo.GetPage(),
		PageSize:  pageInfo.GetPageSize(),
		SortBy:    "created_at",
		SortOrder: "desc",
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}

	filtered := make([]ionet.Deployment, 0, len(dl.Deployments))
	if keyword == "" {
		filtered = dl.Deployments
	} else {
		kw := strings.ToLower(keyword)
		for _, d := range dl.Deployments {
			if strings.Contains(strings.ToLower(d.Name), kw) {
				filtered = append(filtered, d)
			}
		}
	}

	items := make([]map[string]any, 0, len(filtered))
	for _, d := range filtered {
		items = append(items, mapIoNetDeployment(d))
	}

	total := dl.Total
	if keyword != "" {
		total = len(filtered)
	}

	data := gin.H{
		"page":      pageInfo.GetPage(),
		"page_size": pageInfo.GetPageSize(),
		"total":     total,
		"items":     items,
	}
	common.ApiSuccess(c, data)
}

func GetDeployment(c *gin.Context) {
	client, ok := getIoEnterpriseClient(c)
	if !ok {
		return
	}

	deploymentID, ok := requireDeploymentID(c)
	if !ok {
		return
	}

	details, err := client.GetDeployment(deploymentID)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	data := map[string]any{
		"id":              details.ID,
		"deployment_name": details.ID,
		"model_name":      "",
		"model_version":   "",
		"status":          strings.ToLower(details.Status),
		"instance_count":  details.TotalContainers,
		"hardware_id":     details.HardwareID,
		"resource_config": map[string]any{
			"cpu":    "",
			"memory": "",
			"gpu":    strconv.Itoa(details.TotalGPUs),
		},
		"created_at":                details.CreatedAt.Unix(),
		"updated_at":                details.CreatedAt.Unix(),
		"description":               "",
		"amount_paid":               details.AmountPaid,
		"completed_percent":         details.CompletedPercent,
		"gpus_per_container":        details.GPUsPerContainer,
		"total_gpus":                details.TotalGPUs,
		"total_containers":          details.TotalContainers,
		"hardware_name":             details.HardwareName,
		"brand_name":                details.BrandName,
		"compute_minutes_served":    details.ComputeMinutesServed,
		"compute_minutes_remaining": details.ComputeMinutesRemaining,
		"locations":                 details.Locations,
		"container_config":          details.ContainerConfig,
	}

	common.ApiSuccess(c, data)
}

func UpdateDeploymentName(c *gin.Context) {
	client, ok := getIoEnterpriseClient(c)
	if !ok {
		return
	}

	deploymentID, ok := requireDeploymentID(c)
	if !ok {
		return
	}

	var req struct {
		Name string `json:"name" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}

	updateReq := &ionet.UpdateClusterNameRequest{
		Name: strings.TrimSpace(req.Name),
	}

	if updateReq.Name == "" {
		common.ApiErrorMsg(c, "deployment name cannot be empty")
		return
	}

	available, err := client.CheckClusterNameAvailability(updateReq.Name)
	if err != nil {
		common.ApiError(c, fmt.Errorf("failed to check name availability: %w", err))
		return
	}

	if !available {
		common.ApiErrorMsg(c, "deployment name is not available, please choose a different name")
		return
	}

	resp, err := client.UpdateClusterName(deploymentID, updateReq)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	data := gin.H{
		"status":  resp.Status,
		"message": resp.Message,
		"id":      deploymentID,
		"name":    updateReq.Name,
	}
	common.ApiSuccess(c, data)
}

func UpdateDeployment(c *gin.Context) {
	client, ok := getIoEnterpriseClient(c)
	if !ok {
		return
	}

	deploymentID, ok := requireDeploymentID(c)
	if !ok {
		return
	}

	var req ionet.UpdateDeploymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}

	resp, err := client.UpdateDeployment(deploymentID, &req)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	data := gin.H{
		"status":        resp.Status,
		"deployment_id": resp.DeploymentID,
	}
	common.ApiSuccess(c, data)
}

func ExtendDeployment(c *gin.Context) {
	client, ok := getIoEnterpriseClient(c)
	if !ok {
		return
	}

	deploymentID, ok := requireDeploymentID(c)
	if !ok {
		return
	}

	var req ionet.ExtendDurationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}

	details, err := client.ExtendDeployment(deploymentID, &req)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	data := mapIoNetDeployment(ionet.Deployment{
		ID:                      details.ID,
		Status:                  details.Status,
		Name:                    deploymentID,
		CompletedPercent:        float64(details.CompletedPercent),
		HardwareQuantity:        details.TotalGPUs,
		BrandName:               details.BrandName,
		HardwareName:            details.HardwareName,
		ComputeMinutesServed:    details.ComputeMinutesServed,
		ComputeMinutesRemaining: details.ComputeMinutesRemaining,
		CreatedAt:               details.CreatedAt,
	})

	common.ApiSuccess(c, data)
}

func DeleteDeployment(c *gin.Context) {
	client, ok := getIoEnterpriseClient(c)
	if !ok {
		return
	}

	deploymentID, ok := requireDeploymentID(c)
	if !ok {
		return
	}

	resp, err := client.DeleteDeployment(deploymentID)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	data := gin.H{
		"status":        resp.Status,
		"deployment_id": resp.DeploymentID,
		"message":       "Deployment termination requested successfully",
	}
	common.ApiSuccess(c, data)
}

func CreateDeployment(c *gin.Context) {
	client, ok := getIoEnterpriseClient(c)
	if !ok {
		return
	}

	var req ionet.DeploymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}

	resp, err := client.DeployContainer(&req)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	data := gin.H{
		"deployment_id": resp.DeploymentID,
		"status":        resp.Status,
		"message":       "Deployment created successfully",
	}
	common.ApiSuccess(c, data)
}

func GetHardwareTypes(c *gin.Context) {
	client, ok := getIoEnterpriseClient(c)
	if !ok {
		return
	}

	hardwareTypes, totalAvailable, err := client.ListHardwareTypes()
	if err != nil {
		common.ApiError(c, err)
		return
	}

	data := gin.H{
		"hardware_types":  hardwareTypes,
		"total":           len(hardwareTypes),
		"total_available": totalAvailable,
	}
	common.ApiSuccess(c, data)
}

func GetLocations(c *gin.Context) {
	client, ok := getIoClient(c)
	if !ok {
		return
	}

	locationsResp, err := client.ListLocations()
	if err != nil {
		common.ApiError(c, err)
		return
	}

	total := locationsResp.Total
	if total == 0 {
		total = len(locationsResp.Locations)
	}

	data := gin.H{
		"locations": locationsResp.Locations,
		"total":     total,
	}
	common.ApiSuccess(c, data)
}

func GetAvailableReplicas(c *gin.Context) {
	client, ok := getIoEnterpriseClient(c)
	if !ok {
		return
	}

	hardwareIDStr := c.Query("hardware_id")
	gpuCountStr := c.Query("gpu_count")

	if hardwareIDStr == "" {
		common.ApiErrorMsg(c, "hardware_id parameter is required")
		return
	}

	hardwareID, err := strconv.Atoi(hardwareIDStr)
	if err != nil || hardwareID <= 0 {
		common.ApiErrorMsg(c, "invalid hardware_id parameter")
		return
	}

	gpuCount := 1
	if gpuCountStr != "" {
		if parsed, err := strconv.Atoi(gpuCountStr); err == nil && parsed > 0 {
			gpuCount = parsed
		}
	}

	replicas, err := client.GetAvailableReplicas(hardwareID, gpuCount)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	common.ApiSuccess(c, replicas)
}

func GetPriceEstimation(c *gin.Context) {
	client, ok := getIoEnterpriseClient(c)
	if !ok {
		return
	}

	var req ionet.PriceEstimationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}

	priceResp, err := client.GetPriceEstimation(&req)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	common.ApiSuccess(c, priceResp)
}

func CheckClusterNameAvailability(c *gin.Context) {
	client, ok := getIoEnterpriseClient(c)
	if !ok {
		return
	}

	clusterName := strings.TrimSpace(c.Query("name"))
	if clusterName == "" {
		common.ApiErrorMsg(c, "name parameter is required")
		return
	}

	available, err := client.CheckClusterNameAvailability(clusterName)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	data := gin.H{
		"available": available,
		"name":      clusterName,
	}
	common.ApiSuccess(c, data)
}

func GetDeploymentLogs(c *gin.Context) {
	client, ok := getIoClient(c)
	if !ok {
		return
	}

	deploymentID, ok := requireDeploymentID(c)
	if !ok {
		return
	}

	containerID := c.Query("container_id")
	if containerID == "" {
		common.ApiErrorMsg(c, "container_id parameter is required")
		return
	}
	level := c.Query("level")
	stream := c.Query("stream")
	cursor := c.Query("cursor")
	limitStr := c.Query("limit")
	follow := c.Query("follow") == "true"

	var limit int = 100
	if limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 {
			limit = min(parsedLimit, 1000)
		}
	}

	opts := &ionet.GetLogsOptions{
		Level:  level,
		Stream: stream,
		Limit:  limit,
		Cursor: cursor,
		Follow: follow,
	}

	if startTime := c.Query("start_time"); startTime != "" {
		if t, err := time.Parse(time.RFC3339, startTime); err == nil {
			opts.StartTime = &t
		}
	}
	if endTime := c.Query("end_time"); endTime != "" {
		if t, err := time.Parse(time.RFC3339, endTime); err == nil {
			opts.EndTime = &t
		}
	}

	rawLogs, err := client.GetContainerLogsRaw(deploymentID, containerID, opts)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	common.ApiSuccess(c, rawLogs)
}

func ListDeploymentContainers(c *gin.Context) {
	client, ok := getIoEnterpriseClient(c)
	if !ok {
		return
	}

	deploymentID, ok := requireDeploymentID(c)
	if !ok {
		return
	}

	containers, err := client.ListContainers(deploymentID)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	items := make([]map[string]any, 0)
	if containers != nil {
		items = make([]map[string]any, 0, len(containers.Workers))
		for _, ctr := range containers.Workers {
			events := make([]map[string]any, 0, len(ctr.ContainerEvents))
			for _, event := range ctr.ContainerEvents {
				events = append(events, map[string]any{
					"time":    event.Time.Unix(),
					"message": event.Message,
				})
			}

			items = append(items, map[string]any{
				"container_id":       ctr.ContainerID,
				"device_id":          ctr.DeviceID,
				"status":             strings.ToLower(strings.TrimSpace(ctr.Status)),
				"hardware":           ctr.Hardware,
				"brand_name":         ctr.BrandName,
				"created_at":         ctr.CreatedAt.Unix(),
				"uptime_percent":     ctr.UptimePercent,
				"gpus_per_container": ctr.GPUsPerContainer,
				"public_url":         ctr.PublicURL,
				"events":             events,
			})
		}
	}

	response := gin.H{
		"total":      0,
		"containers": items,
	}
	if containers != nil {
		response["total"] = containers.Total
	}

	common.ApiSuccess(c, response)
}

func GetContainerDetails(c *gin.Context) {
	client, ok := getIoEnterpriseClient(c)
	if !ok {
		return
	}

	deploymentID, ok := requireDeploymentID(c)
	if !ok {
		return
	}

	containerID, ok := requireContainerID(c)
	if !ok {
		return
	}

	details, err := client.GetContainerDetails(deploymentID, containerID)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if details == nil {
		common.ApiErrorMsg(c, "container details not found")
		return
	}

	events := make([]map[string]any, 0, len(details.ContainerEvents))
	for _, event := range details.ContainerEvents {
		events = append(events, map[string]any{
			"time":    event.Time.Unix(),
			"message": event.Message,
		})
	}

	data := gin.H{
		"deployment_id":      deploymentID,
		"container_id":       details.ContainerID,
		"device_id":          details.DeviceID,
		"status":             strings.ToLower(strings.TrimSpace(details.Status)),
		"hardware":           details.Hardware,
		"brand_name":         details.BrandName,
		"created_at":         details.CreatedAt.Unix(),
		"uptime_percent":     details.UptimePercent,
		"gpus_per_container": details.GPUsPerContainer,
		"public_url":         details.PublicURL,
		"events":             events,
	}

	common.ApiSuccess(c, data)
}
