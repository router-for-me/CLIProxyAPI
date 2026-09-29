// Command fetch_devin_models connects to the Devin/Codeium Connect-RPC API using
// stored auth credentials and saves the dynamically fetched model list to a
// JSON file for inspection or offline catalog updates.
//
// Usage:
//
//	go run ./cmd/fetch_devin_models [flags]
//
// Flags:
//
//	--auths-dir <path>  Directory containing auth JSON files (default: config auth-dir)
//	--config    <path>  Config file path                 (default: "config.yaml")
//	--output    <path>  Output JSON file path             (default: "devin_models.json")
//	--raw               Dump all raw variants without aggregating thinking levels (default: false)
//	--pretty            Pretty-print the output JSON      (default: true)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	devinGetCliModelConfigsURL = "https://server.codeium.com/exa.api_server_pb.ApiServerService/GetCliModelConfigs"
	devinDefaultUserAgent      = "connect-go/1.19.1 (go1.25.0)"
)

func init() {
	logging.SetupBaseLogger()
	log.SetLevel(log.InfoLevel)
}

type devinCatalogOutput struct {
	Devin []devinModelJSON `json:"devin"`
}

type devinModelJSON struct {
	ID                        string             `json:"id"`
	Object                    string             `json:"object"`
	Type                      string             `json:"type"`
	OwnedBy                   string             `json:"owned_by"`
	DisplayName               string             `json:"display_name"`
	ContextLength             int                `json:"context_length,omitempty"`
	MaxCompletionTokens       int                `json:"max_completion_tokens,omitempty"`
	SupportedInputModalities  []string           `json:"supportedInputModalities,omitempty"`
	SupportedOutputModalities []string           `json:"supportedOutputModalities,omitempty"`
	Thinking                  *devinThinkingJSON `json:"thinking,omitempty"`
}

type devinThinkingJSON struct {
	Levels []string `json:"levels,omitempty"`
}

type rawDevinModel struct {
	UID           string
	Label         string
	ContextLength int
	Multimodal    bool
	VendorID      uint64
	EffortTier    string
}

func main() {
	var authsDir string
	var configPath string
	var outputPath string
	var rawOutput bool
	var pretty bool

	flag.StringVar(&authsDir, "auths-dir", "", "Directory containing auth JSON files (overrides config auth-dir)")
	flag.StringVar(&configPath, "config", "", "Configure File Path")
	flag.StringVar(&outputPath, "output", "devin_models.json", "Output JSON file path")
	flag.BoolVar(&rawOutput, "raw", false, "Dump all raw model configurations without aggregation")
	flag.BoolVar(&pretty, "pretty", true, "Pretty-print the output JSON")
	flag.Parse()

	authsDirOverridden := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "auths-dir" {
			authsDirOverridden = true
		}
	})

	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	if strings.TrimSpace(configPath) == "" {
		configPath = filepath.Join(wd, "config.yaml")
	}
	cfg, err := config.LoadConfigOptional(configPath, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to load config file %s: %v\n", configPath, err)
		os.Exit(1)
	}
	if cfg == nil {
		cfg = &config.Config{}
	}

	if !authsDirOverridden {
		authsDir = cfg.AuthDir
	} else if strings.TrimSpace(authsDir) != "" && !strings.HasPrefix(strings.TrimSpace(authsDir), "~") && !filepath.IsAbs(authsDir) {
		authsDir = filepath.Join(wd, authsDir)
	}
	if authsDir, err = util.ResolveAuthDir(authsDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to resolve auth directory: %v\n", err)
		os.Exit(1)
	}
	if !filepath.IsAbs(outputPath) {
		outputPath = filepath.Join(wd, outputPath)
	}

	fmt.Printf("Scanning auth files in: %s\n", authsDir)

	fileStore := sdkauth.NewFileTokenStore()
	fileStore.SetBaseDir(authsDir)

	ctx := context.Background()
	authList, err := fileStore.List(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to list auth files: %v\n", err)
		os.Exit(1)
	}

	var chosen *coreauth.Auth
	for _, a := range authList {
		if a == nil || a.Disabled {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(a.Provider), "devin") {
			chosen = a
			break
		}
	}
	if chosen == nil {
		fmt.Fprintf(os.Stderr, "error: no enabled devin auth found in %s\n", authsDir)
		os.Exit(1)
	}

	apiKey := ""
	if chosen.Attributes != nil {
		apiKey = chosen.Attributes["api_key"]
	}
	if apiKey == "" && chosen.Metadata != nil {
		if val, ok := chosen.Metadata["api_key"].(string); ok {
			apiKey = val
		}
	}
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "error: devin auth %s has no api_key\n", chosen.ID)
		os.Exit(1)
	}

	fmt.Printf("Using auth: id=%s label=%s\n", chosen.ID, chosen.Label)
	fmt.Println("Fetching Devin model catalog from upstream...")

	rawModels, err := fetchRawDevinModels(ctx, cfg, chosen, apiKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: fetch failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully fetched %d raw model configs from upstream.\n", len(rawModels))

	var outputJSON devinCatalogOutput
	if rawOutput {
		outputJSON.Devin = formatRawModels(rawModels)
	} else {
		outputJSON.Devin = aggregateModels(rawModels)
	}

	var encoded []byte
	if pretty {
		encoded, err = json.MarshalIndent(outputJSON, "", "  ")
	} else {
		encoded, err = json.Marshal(outputJSON)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to encode JSON: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to create output directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(outputPath, encoded, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to write output file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Catalog written to: %s (%d models)\n", outputPath, len(outputJSON.Devin))
}

func fetchRawDevinModels(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, apiKey string) ([]rawDevinModel, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	f1Bytes := helps.BuildDevinClientMetadataBytes(apiKey, "", "")
	var reqBody []byte
	reqBody = protowire.AppendTag(reqBody, 1, protowire.BytesType)
	reqBody = protowire.AppendBytes(reqBody, f1Bytes)

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodPost, devinGetCliModelConfigsURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Basic "+apiKey+"-"+apiKey)
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("User-Agent", devinDefaultUserAgent)

	httpClient := helps.NewProxyAwareHTTPClient(fetchCtx, cfg, auth, 30*time.Second)
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("failed to close response body: %v", errClose)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		bodySnippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("upstream returned status %d: %s", resp.StatusCode, proxyutil.Redact(string(bodySnippet)))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	return parseRawDevinModelsProto(respBytes)
}

func parseRawDevinModelsProto(b []byte) ([]rawDevinModel, error) {
	var results []rawDevinModel
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, errors.New("malformed protobuf tag")
		}
		b = b[n:]

		if num == 1 && typ == protowire.BytesType {
			subBytes, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return nil, errors.New("malformed protobuf submessage")
			}
			b = b[m:]

			model := parseSingleModelConfig(subBytes)
			if model.UID != "" {
				results = append(results, model)
			}
		} else {
			skip := protowire.ConsumeFieldValue(num, typ, b)
			if skip < 0 {
				return nil, errors.New("failed to skip field")
			}
			b = b[skip:]
		}
	}
	return results, nil
}

func parseSingleModelConfig(b []byte) rawDevinModel {
	var m rawDevinModel
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]

		switch num {
		case 1: // label
			if typ == protowire.BytesType {
				val, mLen := protowire.ConsumeString(b)
				if mLen >= 0 {
					m.Label = val
					b = b[mLen:]
					continue
				}
			}
		case 5: // multimodal
			if typ == protowire.VarintType {
				val, mLen := protowire.ConsumeVarint(b)
				if mLen >= 0 {
					m.Multimodal = (val == 1)
					b = b[mLen:]
					continue
				}
			}
		case 10: // vendor
			if typ == protowire.VarintType {
				val, mLen := protowire.ConsumeVarint(b)
				if mLen >= 0 {
					m.VendorID = val
					b = b[mLen:]
					continue
				}
			}
		case 18: // context length
			if typ == protowire.VarintType {
				val, mLen := protowire.ConsumeVarint(b)
				if mLen >= 0 {
					m.ContextLength = int(val)
					b = b[mLen:]
					continue
				}
			}
		case 22: // chat_model_uid
			if typ == protowire.BytesType {
				val, mLen := protowire.ConsumeString(b)
				if mLen >= 0 {
					m.UID = val
					b = b[mLen:]
					continue
				}
			}
		}

		skip := protowire.ConsumeFieldValue(num, typ, b)
		if skip < 0 {
			break
		}
		b = b[skip:]
	}
	return m
}

func vendorName(id uint64, uid string) string {
	switch id {
	case 1:
		return "cognition"
	case 2:
		return "openai"
	case 3:
		return "anthropic"
	case 4:
		return "google"
	case 6:
		return "deepseek"
	case 7:
		return "moonshot"
	case 9:
		return "zhipu"
	case 11:
		return "nvidia"
	default:
		if strings.Contains(strings.ToLower(uid), "grok") {
			return "xai"
		}
		return "devin"
	}
}

func formatRawModels(raw []rawDevinModel) []devinModelJSON {
	res := make([]devinModelJSON, 0, len(raw))
	for _, r := range raw {
		modalities := []string{"text"}
		if r.Multimodal {
			modalities = append(modalities, "image")
		}
		res = append(res, devinModelJSON{
			ID:                        r.UID,
			Object:                    "model",
			Type:                      "devin",
			OwnedBy:                   vendorName(r.VendorID, r.UID),
			DisplayName:               r.Label,
			ContextLength:             r.ContextLength,
			MaxCompletionTokens:       64000,
			SupportedInputModalities:  modalities,
			SupportedOutputModalities: []string{"text"},
		})
	}
	return res
}

var devinCompoundSuffixes = []struct {
	suffix string
	effort string
	readd  string
}{
	{suffix: "-low-fast", effort: "low"},
	{suffix: "-medium-fast", effort: "medium"},
	{suffix: "-high-fast", effort: "high"},
	{suffix: "-xhigh-fast", effort: "xhigh"},
	{suffix: "-max-fast", effort: "max"},
	{suffix: "-none-fast", effort: "none"},
	{suffix: "-low-priority", effort: "low"},
	{suffix: "-medium-priority", effort: "medium"},
	{suffix: "-high-priority", effort: "high"},
	{suffix: "-xhigh-priority", effort: "xhigh"},
	{suffix: "-max-priority", effort: "max"},
	{suffix: "-none-priority", effort: "none"},
	{suffix: "-thinking-1m", effort: "", readd: "-1m"},
	{suffix: "-thinking", effort: ""},
	{suffix: "-max-1m", effort: "max", readd: "-1m"},
	{suffix: "-none-1m", effort: "none", readd: "-1m"},
}

var devinSimpleEffortSuffixes = []struct {
	suffix string
	effort string
}{
	{suffix: "-none", effort: "none"},
	{suffix: "-minimal", effort: "minimal"},
	{suffix: "-low", effort: "low"},
	{suffix: "-medium", effort: "medium"},
	{suffix: "-high", effort: "high"},
	{suffix: "-xhigh", effort: "xhigh"},
	{suffix: "-max", effort: "max"},
}

func splitDevinUID(uid string) (string, string) {
	if uid == "swe-1-6-slow" {
		return uid, ""
	}
	if uid == "swe-1-6-fast" {
		return "swe-1-6", ""
	}

	upper := strings.ToUpper(uid)
	for _, s := range []struct {
		suffix string
		effort string
	}{
		{"_NONE", "none"},
		{"_MINIMAL", "minimal"},
		{"_LOW", "low"},
		{"_MEDIUM", "medium"},
		{"_HIGH", "high"},
		{"_XHIGH", "xhigh"},
		{"_MAX", "max"},
		{"_THINKING", "high"},
	} {
		if strings.HasSuffix(upper, s.suffix) {
			base := uid[:len(uid)-len(s.suffix)]
			return base, s.effort
		}
	}

	for _, s := range devinCompoundSuffixes {
		if strings.HasSuffix(uid, s.suffix) {
			base := uid[:len(uid)-len(s.suffix)]
			if s.readd != "" {
				base += s.readd
			}
			return base, s.effort
		}
	}

	for _, s := range devinSimpleEffortSuffixes {
		if strings.HasSuffix(uid, s.suffix) {
			base := uid[:len(uid)-len(s.suffix)]
			return base, s.effort
		}
	}

	return uid, ""
}

func aggregateModels(raw []rawDevinModel) []devinModelJSON {
	type aggEntry struct {
		baseID        string
		displayName   string
		vendorID      uint64
		contextLength int
		multimodal    bool
		levels        map[string]struct{}
	}

	grouped := make(map[string]*aggEntry)
	var order []string

	for _, r := range raw {
		base, level := splitDevinUID(r.UID)
		if base == "" {
			base = r.UID
		}
		isBase := (base == r.UID)

		entry, exists := grouped[base]
		if !exists {
			initialLevels := make(map[string]struct{})
			if mInfo := registry.LookupDevinModel(base); mInfo != nil && mInfo.Thinking != nil {
				for _, l := range mInfo.Thinking.Levels {
					if l != "" && l != "priority" {
						initialLevels[l] = struct{}{}
					}
				}
			}
			entry = &aggEntry{
				baseID:        base,
				displayName:   cleanDisplayName(r.Label),
				vendorID:      r.VendorID,
				contextLength: r.ContextLength,
				multimodal:    r.Multimodal,
				levels:        initialLevels,
			}
			grouped[base] = entry
			order = append(order, base)
		}

		if isBase {
			entry.displayName = cleanDisplayName(r.Label)
			if r.VendorID != 0 {
				entry.vendorID = r.VendorID
			}
		}
		if r.Multimodal {
			entry.multimodal = true
		}
		if r.ContextLength > entry.contextLength {
			entry.contextLength = r.ContextLength
		}
		if level != "" && level != "priority" {
			entry.levels[level] = struct{}{}
		}
	}

	res := make([]devinModelJSON, 0, len(order))
	for _, id := range order {
		entry := grouped[id]
		modalities := []string{"text"}
		if entry.multimodal {
			modalities = append(modalities, "image")
		}

		var thinking *devinThinkingJSON
		if len(entry.levels) > 0 {
			levelsList := make([]string, 0, len(entry.levels))
			for l := range entry.levels {
				levelsList = append(levelsList, l)
			}
			sortLevels(levelsList)
			thinking = &devinThinkingJSON{
				Levels: levelsList,
			}
		}

		res = append(res, devinModelJSON{
			ID:                        entry.baseID,
			Object:                    "model",
			Type:                      "devin",
			OwnedBy:                   vendorName(entry.vendorID, entry.baseID),
			DisplayName:               entry.displayName,
			ContextLength:             entry.contextLength,
			MaxCompletionTokens:       64000,
			SupportedInputModalities:  modalities,
			SupportedOutputModalities: []string{"text"},
			Thinking:                  thinking,
		})
	}

	return res
}

var devinDisplayNameSuffixes = []string{
	" Low Fast", " Medium Fast", " High Fast", " XHigh Fast", " Max Fast",
	" Low Thinking Fast", " Medium Thinking Fast", " High Thinking Fast",
	" XHigh Thinking Fast", " Max Thinking Fast", " No Thinking Fast",
	" Low Thinking", " Medium Thinking", " High Thinking", " XHigh Thinking",
	" Max Thinking", " No Thinking",
	" Low", " Medium", " High", " XHigh", " Max", " None", " Minimal",
	" Thinking", " Fast",
}

func cleanDisplayName(label string) string {
	trimmed := strings.TrimSpace(label)
	for {
		changed := false
		for _, s := range devinDisplayNameSuffixes {
			if strings.HasSuffix(strings.ToLower(trimmed), strings.ToLower(s)) {
				trimmed = strings.TrimSpace(trimmed[:len(trimmed)-len(s)])
				changed = true
				break
			}
		}
		if !changed {
			break
		}
	}
	return trimmed
}

func sortLevels(levels []string) {
	rank := map[string]int{
		"none":     0,
		"minimal":  1,
		"low":      2,
		"medium":   3,
		"high":     4,
		"xhigh":    5,
		"max":      6,
		"fast":     7,
		"priority": 8,
	}
	sort.Slice(levels, func(i, j int) bool {
		rI, okI := rank[levels[i]]
		if !okI {
			rI = 99
		}
		rJ, okJ := rank[levels[j]]
		if !okJ {
			rJ = 99
		}
		if rI != rJ {
			return rI < rJ
		}
		return levels[i] < levels[j]
	})
}
