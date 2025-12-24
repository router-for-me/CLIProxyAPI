// Package cmd provides command-line interface functionality for the CLI Proxy API server.
// It includes authentication flows for various AI service providers, service startup,
// and other command-line operations.
package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/gemini"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	geminiCLIEndpoint       = "https://cloudcode-pa.googleapis.com"
	geminiCLIVersion        = "v1internal"
	geminiCLIUserAgent      = "google-api-nodejs-client/9.15.1"
	geminiCLIApiClient      = "gl-node/22.17.0"
	geminiCLIClientMetadata = "ideType=IDE_UNSPECIFIED,platform=PLATFORM_UNSPECIFIED,pluginType=GEMINI"
)

type projectSelectionRequiredError struct{}

func (e *projectSelectionRequiredError) Error() string {
	return "gemini cli: project selection required"
}

// DoLogin handles Google Gemini authentication using the shared authentication manager.
// It initiates the OAuth flow for Google Gemini services, performs the legacy CLI user setup,
// and saves the authentication tokens to the configured auth directory.
//
// Parameters:
//   - cfg: The application configuration
//   - projectID: Optional Google Cloud project ID for Gemini services
//   - options: Login options including browser behavior and prompts
func DoLogin(cfg *config.Config, projectID string, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	ctx := context.Background()

	promptFn := options.Prompt
	if promptFn == nil {
		promptFn = defaultProjectPrompt()
	}

	trimmedProjectID := strings.TrimSpace(projectID)
	callbackPrompt := promptFn
	if trimmedProjectID == "" {
		callbackPrompt = nil
	}

	loginOpts := &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		ProjectID: trimmedProjectID,
		Metadata:  map[string]string{},
		Prompt:    callbackPrompt,
	}

	authenticator := sdkAuth.NewGeminiAuthenticator()
	record, errLogin := authenticator.Login(ctx, cfg, loginOpts)
	if errLogin != nil {
		log.Errorf("Gemini authentication failed: %v", errLogin)
		return
	}

	storage, okStorage := record.Storage.(*gemini.GeminiTokenStorage)
	if !okStorage || storage == nil {
		log.Error("Gemini authentication failed: unsupported token storage")
		return
	}

	geminiAuth := gemini.NewGeminiAuth()
	httpClient, errClient := geminiAuth.GetAuthenticatedClient(ctx, storage, cfg, &gemini.WebLoginOptions{
		NoBrowser: options.NoBrowser,
		Prompt:    callbackPrompt,
	})
	if errClient != nil {
		log.Errorf("Gemini authentication failed: %v", errClient)
		return
	}

	log.Info("Authentication successful.")

	projects, errProjects := fetchGCPProjects(ctx, httpClient)
	if errProjects != nil {
		log.Errorf("Failed to get project list: %v", errProjects)
		return
	}

	selectedProjectID := promptForProjectSelection(projects, trimmedProjectID, promptFn)
	projectSelections, errSelection := resolveProjectSelections(selectedProjectID, projects)
	if errSelection != nil {
		log.Errorf("Invalid project selection: %v", errSelection)
		return
	}
	if len(projectSelections) == 0 {
		log.Error("No project selected; aborting login.")
		return
	}

	activatedProjects := make([]string, 0, len(projectSelections))
	for _, candidateID := range projectSelections {
		log.Infof("Activating project %s", candidateID)
		if errSetup := performGeminiCLISetup(ctx, httpClient, storage, candidateID); errSetup != nil {
			var projectErr *projectSelectionRequiredError
			if errors.As(errSetup, &projectErr) {
				log.Error("Failed to start user onboarding: A project ID is required.")
				showProjectSelectionHelp(storage.Email, projects)
				return
			}
			log.Errorf("Failed to complete user setup: %v", errSetup)
			return
		}
		finalID := strings.TrimSpace(storage.ProjectID)
		if finalID == "" {
			finalID = candidateID
		}
		activatedProjects = append(activatedProjects, finalID)
	}

	storage.Auto = false
	storage.ProjectID = strings.Join(activatedProjects, ",")

	if !storage.Auto && !storage.Checked {
		for _, pid := range activatedProjects {
			isChecked, errCheck := checkCloudAPIIsEnabled(ctx, httpClient, pid)
			if errCheck != nil {
				log.Errorf("Failed to check if Cloud AI API is enabled for %s: %v", pid, errCheck)
				return
			}
			if !isChecked {
				log.Errorf("Failed to check if Cloud AI API is enabled for project %s. If you encounter an error message, please create an issue.", pid)
				return
			}
		}
		storage.Checked = true
	}

	updateAuthRecord(record, storage)

	store := sdkAuth.GetTokenStore()
	if setter, okSetter := store.(interface{ SetBaseDir(string) }); okSetter && cfg != nil {
		setter.SetBaseDir(cfg.AuthDir)
	}

	savedPath, errSave := store.Save(ctx, record)
	if errSave != nil {
		log.Errorf("Failed to save token to file: %v", errSave)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}

	fmt.Println("Gemini authentication successful!")
}

func performGeminiCLISetup(ctx context.Context, httpClient *http.Client, storage *gemini.GeminiTokenStorage, requestedProject string) error {
	metadata := map[string]string{
		"ideType":    "IDE_UNSPECIFIED",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
	}

	trimmedRequest := strings.TrimSpace(requestedProject)
	explicitProject := trimmedRequest != ""

	loadReqBody := map[string]any{
		"metadata": metadata,
	}
	if explicitProject {
		loadReqBody["cloudaicompanionProject"] = trimmedRequest
	}

	var loadResp map[string]any
	if errLoad := callGeminiCLI(ctx, httpClient, "loadCodeAssist", loadReqBody, &loadResp); errLoad != nil {
		return fmt.Errorf("load code assist: %w", errLoad)
	}

	tierID := "legacy-tier"
	if tiers, okTiers := loadResp["allowedTiers"].([]any); okTiers {
		for _, rawTier := range tiers {
			tier, okTier := rawTier.(map[string]any)
			if !okTier {
				continue
			}
			if isDefault, okDefault := tier["isDefault"].(bool); okDefault && isDefault {
				if id, okID := tier["id"].(string); okID && strings.TrimSpace(id) != "" {
					tierID = strings.TrimSpace(id)
					break
				}
			}
		}
	}

	projectID := trimmedRequest
	if projectID == "" {
		if id, okProject := loadResp["cloudaicompanionProject"].(string); okProject {
			projectID = strings.TrimSpace(id)
		}
		if projectID == "" {
			if projectMap, okProject := loadResp["cloudaicompanionProject"].(map[string]any); okProject {
				if id, okID := projectMap["id"].(string); okID {
					projectID = strings.TrimSpace(id)
				}
			}
		}
	}
	if projectID == "" {
		return &projectSelectionRequiredError{}
	}

	onboardReqBody := map[string]any{
		"tierId":                  tierID,
		"metadata":                metadata,
		"cloudaicompanionProject": projectID,
	}

	// Store the requested project as a fallback in case the response omits it.
	storage.ProjectID = projectID

	for {
		var onboardResp map[string]any
		if errOnboard := callGeminiCLI(ctx, httpClient, "onboardUser", onboardReqBody, &onboardResp); errOnboard != nil {
			return fmt.Errorf("onboard user: %w", errOnboard)
		}

		if done, okDone := onboardResp["done"].(bool); okDone && done {
			responseProjectID := ""
			if resp, okResp := onboardResp["response"].(map[string]any); okResp {
				switch projectValue := resp["cloudaicompanionProject"].(type) {
				case map[string]any:
					if id, okID := projectValue["id"].(string); okID {
						responseProjectID = strings.TrimSpace(id)
					}
				case string:
					responseProjectID = strings.TrimSpace(projectValue)
				}
			}

			finalProjectID := projectID
			if responseProjectID != "" {
				if explicitProject && !strings.EqualFold(responseProjectID, projectID) {
					log.Warnf("Gemini onboarding returned project %s instead of requested %s; keeping requested project ID.", responseProjectID, projectID)
				} else {
					finalProjectID = responseProjectID
				}
			}

			storage.ProjectID = strings.TrimSpace(finalProjectID)
			if storage.ProjectID == "" {
				storage.ProjectID = strings.TrimSpace(projectID)
			}
			if storage.ProjectID == "" {
				return fmt.Errorf("onboard user completed without project id")
			}
			log.Infof("Onboarding complete. Using Project ID: %s", storage.ProjectID)
			return nil
		}

		log.Println("Onboarding in progress, waiting 5 seconds...")
		time.Sleep(5 * time.Second)
	}
}

func callGeminiCLI(ctx context.Context, httpClient *http.Client, endpoint string, body any, result any) error {
	url := fmt.Sprintf("%s/%s:%s", geminiCLIEndpoint, geminiCLIVersion, endpoint)
	if strings.HasPrefix(endpoint, "operations/") {
		url = fmt.Sprintf("%s/%s", geminiCLIEndpoint, endpoint)
	}

	var reader io.Reader
	if body != nil {
		rawBody, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			return fmt.Errorf("marshal request body: %w", errMarshal)
		}
		reader = bytes.NewReader(rawBody)
	}

	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, reader)
	if errRequest != nil {
		return fmt.Errorf("create request: %w", errRequest)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", geminiCLIUserAgent)
	req.Header.Set("X-Goog-Api-Client", geminiCLIApiClient)
	req.Header.Set("Client-Metadata", geminiCLIClientMetadata)

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return fmt.Errorf("execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("api request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	if result == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	if errDecode := json.NewDecoder(resp.Body).Decode(result); errDecode != nil {
		return fmt.Errorf("decode response body: %w", errDecode)
	}

	return nil
}

func fetchGCPProjects(ctx context.Context, httpClient *http.Client) ([]interfaces.GCPProjectProjects, error) {
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, "https://cloudresourcemanager.googleapis.com/v1/projects", nil)
	if errRequest != nil {
		return nil, fmt.Errorf("could not create project list request: %w", errRequest)
	}

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("failed to execute project list request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("project list request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var projects interfaces.GCPProject
	if errDecode := json.NewDecoder(resp.Body).Decode(&projects); errDecode != nil {
		return nil, fmt.Errorf("failed to unmarshal project list: %w", errDecode)
	}

	return projects.Projects, nil
}

// promptForProjectSelection prints available projects and returns the chosen project ID.
func promptForProjectSelection(projects []interfaces.GCPProjectProjects, presetID string, promptFn func(string) (string, error)) string {
	trimmedPreset := strings.TrimSpace(presetID)
	if len(projects) == 0 {
		if trimmedPreset != "" {
			return trimmedPreset
		}
		fmt.Println("No Google Cloud projects are available for selection.")
		return ""
	}

	fmt.Println("Available Google Cloud projects:")
	defaultIndex := 0
	for idx, project := range projects {
		fmt.Printf("[%d] %s (%s)\n", idx+1, project.ProjectID, project.Name)
		if trimmedPreset != "" && project.ProjectID == trimmedPreset {
			defaultIndex = idx
		}
	}
	fmt.Println("Type 'ALL' to onboard every listed project.")

	defaultID := projects[defaultIndex].ProjectID

	if trimmedPreset != "" {
		if strings.EqualFold(trimmedPreset, "ALL") {
			return "ALL"
		}
		for _, project := range projects {
			if project.ProjectID == trimmedPreset {
				return trimmedPreset
			}
		}
		log.Warnf("Provided project ID %s not found in available projects; please choose from the list.", trimmedPreset)
	}

	for {
		promptMsg := fmt.Sprintf("Enter project ID [%s] or ALL: ", defaultID)
		answer, errPrompt := promptFn(promptMsg)
		if errPrompt != nil {
			log.Errorf("Project selection prompt failed: %v", errPrompt)
			return defaultID
		}
		answer = strings.TrimSpace(answer)
		if strings.EqualFold(answer, "ALL") {
			return "ALL"
		}
		if answer == "" {
			return defaultID
		}

		for _, project := range projects {
			if project.ProjectID == answer {
				return project.ProjectID
			}
		}

		if idx, errAtoi := strconv.Atoi(answer); errAtoi == nil {
			if idx >= 1 && idx <= len(projects) {
				return projects[idx-1].ProjectID
			}
		}

		fmt.Println("Invalid selection, enter a project ID or a number from the list.")
	}
}

func resolveProjectSelections(selection string, projects []interfaces.GCPProjectProjects) ([]string, error) {
	trimmed := strings.TrimSpace(selection)
	if trimmed == "" {
		return nil, nil
	}
	available := make(map[string]struct{}, len(projects))
	ordered := make([]string, 0, len(projects))
	for _, project := range projects {
		id := strings.TrimSpace(project.ProjectID)
		if id == "" {
			continue
		}
		if _, exists := available[id]; exists {
			continue
		}
		available[id] = struct{}{}
		ordered = append(ordered, id)
	}
	if strings.EqualFold(trimmed, "ALL") {
		if len(ordered) == 0 {
			return nil, fmt.Errorf("no projects available for ALL selection")
		}
		return append([]string(nil), ordered...), nil
	}
	parts := strings.Split(trimmed, ",")
	selections := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		if len(available) > 0 {
			if _, ok := available[id]; !ok {
				return nil, fmt.Errorf("project %s not found in available projects", id)
			}
		}
		seen[id] = struct{}{}
		selections = append(selections, id)
	}
	return selections, nil
}

func defaultProjectPrompt() func(string) (string, error) {
	reader := bufio.NewReader(os.Stdin)
	return func(prompt string) (string, error) {
		fmt.Print(prompt)
		line, errRead := reader.ReadString('\n')
		if errRead != nil {
			if errors.Is(errRead, io.EOF) {
				return strings.TrimSpace(line), nil
			}
			return "", errRead
		}
		return strings.TrimSpace(line), nil
	}
}

func showProjectSelectionHelp(email string, projects []interfaces.GCPProjectProjects) {
	if email != "" {
		log.Infof("Your account %s needs to specify a project ID.", email)
	} else {
		log.Info("You need to specify a project ID.")
	}

	if len(projects) > 0 {
		fmt.Println("========================================================================")
		for _, p := range projects {
			fmt.Printf("Project ID: %s\n", p.ProjectID)
			fmt.Printf("Project Name: %s\n", p.Name)
			fmt.Println("------------------------------------------------------------------------")
		}
	} else {
		fmt.Println("No active projects were returned for this account.")
	}

	fmt.Printf("Please run this command to login again with a specific project:\n\n%s --login --project_id <project_id>\n", os.Args[0])
}

func checkCloudAPIIsEnabled(ctx context.Context, httpClient *http.Client, projectID string) (bool, error) {
	serviceUsageURL := "https://serviceusage.googleapis.com"
	requiredServices := []string{
		// "geminicloudassist.googleapis.com", // Gemini Cloud Assist API
		"cloudaicompanion.googleapis.com", // Gemini for Google Cloud API
	}
	for _, service := range requiredServices {
		checkUrl := fmt.Sprintf("%s/v1/projects/%s/services/%s", serviceUsageURL, projectID, service)
		req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, checkUrl, nil)
		if errRequest != nil {
			return false, fmt.Errorf("failed to create request: %w", errRequest)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", geminiCLIUserAgent)
		resp, errDo := httpClient.Do(req)
		if errDo != nil {
			return false, fmt.Errorf("failed to execute request: %w", errDo)
		}

		if resp.StatusCode == http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			if gjson.GetBytes(bodyBytes, "state").String() == "ENABLED" {
				_ = resp.Body.Close()
				continue
			}
		}
		_ = resp.Body.Close()

		enableUrl := fmt.Sprintf("%s/v1/projects/%s/services/%s:enable", serviceUsageURL, projectID, service)
		req, errRequest = http.NewRequestWithContext(ctx, http.MethodPost, enableUrl, strings.NewReader("{}"))
		if errRequest != nil {
			return false, fmt.Errorf("failed to create request: %w", errRequest)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", geminiCLIUserAgent)
		resp, errDo = httpClient.Do(req)
		if errDo != nil {
			return false, fmt.Errorf("failed to execute request: %w", errDo)
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		errMessage := string(bodyBytes)
		errMessageResult := gjson.GetBytes(bodyBytes, "error.message")
		if errMessageResult.Exists() {
			errMessage = errMessageResult.String()
		}
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
			_ = resp.Body.Close()
			continue
		} else if resp.StatusCode == http.StatusBadRequest {
			_ = resp.Body.Close()
			if strings.Contains(strings.ToLower(errMessage), "already enabled") {
				continue
			}
		}
		_ = resp.Body.Close()
		return false, fmt.Errorf("project activation required: %s", errMessage)
	}
	return true, nil
}

func updateAuthRecord(record *cliproxyauth.Auth, storage *gemini.GeminiTokenStorage) {
	if record == nil || storage == nil {
		return
	}

	finalName := gemini.CredentialFileName(storage.Email, storage.ProjectID, false)

	if record.Metadata == nil {
		record.Metadata = make(map[string]any)
	}
	record.Metadata["email"] = storage.Email
	record.Metadata["project_id"] = storage.ProjectID
	record.Metadata["auto"] = storage.Auto
	record.Metadata["checked"] = storage.Checked

	record.ID = finalName
	record.FileName = finalName
	record.Storage = storage
}
