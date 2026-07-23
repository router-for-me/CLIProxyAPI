package codexintegration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// CommandAction identifies a mutually exclusive Codex Integration command.
type CommandAction string

const (
	CommandSetup   CommandAction = "setup"
	CommandSync    CommandAction = "sync"
	CommandDoctor  CommandAction = "doctor"
	CommandRestore CommandAction = "restore"
)

// CommandOptions controls one-shot Codex Integration commands.
type CommandOptions struct {
	Action           CommandAction
	Apply            bool
	JSON             bool
	ProbeModels      bool
	MigrateOpenCodex bool
	CodexHome        string
	HTTPClient       *http.Client
}

// CommandOutput is the stable machine-readable envelope for lifecycle commands.
type CommandOutput struct {
	SchemaVersion   int           `json:"schema_version"`
	Action          CommandAction `json:"action"`
	Changed         bool          `json:"changed"`
	Applied         bool          `json:"applied"`
	RestartRequired bool          `json:"restart_required"`
	CatalogRevision string        `json:"catalog_revision,omitempty"`
	SourceRevision  uint64        `json:"source_revision,omitempty"`
	MappingRevision string        `json:"mapping_revision,omitempty"`
	ConfigFile      string        `json:"config_file"`
	CatalogFile     string        `json:"catalog_file"`
}

// CommandFailure is the stable machine-readable envelope for command errors.
type CommandFailure struct {
	SchemaVersion int           `json:"schema_version"`
	Action        CommandAction `json:"action"`
	Status        string        `json:"status"`
	Error         string        `json:"error"`
}

// WriteCommandFailure emits a JSON error without including credential contents.
func WriteCommandFailure(output io.Writer, action CommandAction, err error) error {
	message := "Codex Integration command failed"
	if err != nil {
		message = err.Error()
	}
	return writeJSON(output, CommandFailure{SchemaVersion: 1, Action: action, Status: "error", Error: message})
}

// ValidateCommandOptions rejects ambiguous or nonsensical command combinations.
func ValidateCommandOptions(options CommandOptions) error {
	switch options.Action {
	case CommandSetup, CommandSync, CommandDoctor, CommandRestore:
	default:
		return fmt.Errorf("unknown Codex Integration command %q", options.Action)
	}
	if options.Action == CommandDoctor && options.Apply {
		return errors.New("-apply cannot be used with -codex-doctor")
	}
	if options.Action != CommandDoctor && options.ProbeModels {
		return errors.New("-probe-models requires -codex-doctor")
	}
	if options.Action != CommandSetup && options.MigrateOpenCodex {
		return errors.New("-codex-migrate-opencodex requires -codex-setup")
	}
	return nil
}

// RunCommand executes a one-shot command and returns its process exit code.
func RunCommand(ctx context.Context, cfg *config.Config, options CommandOptions, output io.Writer) (int, error) {
	if err := ValidateCommandOptions(options); err != nil {
		return 2, err
	}
	if output == nil {
		output = io.Discard
	}
	lifecycle, err := NewLifecycle(cfg, options.CodexHome)
	if err != nil {
		return 2, err
	}
	models, providers := commandCatalogSource(ctx, lifecycle, options.HTTPClient)
	if options.Action == CommandDoctor {
		report := RunDoctor(ctx, lifecycle, models, providers, DoctorOptions{
			ProbeModels: options.ProbeModels,
			HTTPClient:  options.HTTPClient,
		})
		if options.JSON {
			err = writeJSON(output, report)
		} else {
			err = writeDoctorText(output, report)
		}
		return report.ExitCode(), err
	}

	var result OperationResult
	switch options.Action {
	case CommandSetup:
		result, err = lifecycle.Setup(models, providers, options.Apply, options.MigrateOpenCodex)
	case CommandSync:
		result, err = lifecycle.Sync(models, providers, options.Apply)
	case CommandRestore:
		result, err = lifecycle.Restore(options.Apply)
	}
	if err != nil {
		return 2, err
	}
	envelope := CommandOutput{
		SchemaVersion: 1, Action: options.Action, Changed: result.Changed, Applied: result.Applied,
		RestartRequired: result.RestartRequired, CatalogRevision: result.CatalogRevision,
		SourceRevision: result.SourceRevision, MappingRevision: result.MappingRevision,
		ConfigFile: lifecycle.Paths.ConfigFile, CatalogFile: lifecycle.Paths.CatalogFile,
	}
	if options.JSON {
		return 0, writeJSON(output, envelope)
	}
	mode := "preview"
	if result.Applied {
		mode = "applied"
	} else if !result.Changed {
		mode = "unchanged"
	}
	_, err = fmt.Fprintf(output, "Codex Integration %s: %s\nconfig: %s\ncatalog: %s\nrestart required: %t\n",
		options.Action, mode, lifecycle.Paths.ConfigFile, lifecycle.Paths.CatalogFile, result.RestartRequired)
	return 0, err
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func commandCatalogSource(ctx context.Context, lifecycle *Lifecycle, client *http.Client) ([]map[string]any, ModelProvidersFunc) {
	modelsByID := make(map[string]map[string]any)
	providersByID := make(map[string]map[string]struct{})
	add := func(provider string, infos []*registry.ModelInfo) {
		for _, info := range infos {
			if info == nil || strings.TrimSpace(info.ID) == "" {
				continue
			}
			id := strings.TrimSpace(info.ID)
			if _, exists := modelsByID[id]; !exists {
				modelsByID[id] = commandModelMap(info)
			}
			if providersByID[id] == nil {
				providersByID[id] = make(map[string]struct{})
			}
			providersByID[id][provider] = struct{}{}
		}
	}
	add("codex", registry.GetCodexProModels())
	for _, provider := range config.CodexIntegrationProviders() {
		add(provider, registry.GetStaticModelDefinitionsByChannel(provider))
	}

	liveModels := fetchLiveModelList(ctx, lifecycle.baseURL(), client)
	for _, model := range liveModels {
		id, _ := model["id"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := modelsByID[id]; !exists {
			modelsByID[id] = model
		}
		for _, mapping := range lifecycle.Config.CodexIntegration.Models {
			if mapping.UpstreamModel == id {
				if providersByID[id] == nil {
					providersByID[id] = make(map[string]struct{})
				}
				providersByID[id][mapping.Provider] = struct{}{}
			}
		}
	}

	ids := make([]string, 0, len(modelsByID))
	for id := range modelsByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	models := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		models = append(models, modelsByID[id])
	}
	providers := func(model string) []string {
		set := providersByID[model]
		result := make([]string, 0, len(set))
		for provider := range set {
			result = append(result, provider)
		}
		sort.Strings(result)
		return result
	}
	return models, providers
}

func commandModelMap(info *registry.ModelInfo) map[string]any {
	model := map[string]any{"id": info.ID, "object": "model", "owned_by": info.OwnedBy}
	if info.Created > 0 {
		model["created"] = info.Created
	}
	if info.Type != "" {
		model["type"] = info.Type
	}
	if info.DisplayName != "" {
		model["display_name"] = info.DisplayName
	}
	if info.Description != "" {
		model["description"] = info.Description
	}
	if info.ContextLength > 0 {
		model["context_length"] = info.ContextLength
	}
	if info.MaxCompletionTokens > 0 {
		model["max_completion_tokens"] = info.MaxCompletionTokens
	}
	return model
}

func fetchLiveModelList(ctx context.Context, baseURL string, client *http.Client) []map[string]any {
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil
	}
	request.Header.Set("Authorization", "Bearer codex-integration-local")
	response, err := client.Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if err = decoder.Decode(&payload); err != nil {
		return nil
	}
	return payload.Data
}
