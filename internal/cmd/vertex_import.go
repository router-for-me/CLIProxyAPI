// Package cmd contains CLI helpers. This file implements importing a Vertex AI
// service account JSON into the auth store as a dedicated "vertex" credential.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/vertex"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// DoVertexImport imports a Google Cloud service account key JSON and persists
// it as a "vertex" provider credential. The file content is embedded in the auth
// file to allow portable deployment across stores.
func DoVertexImport(cfg *config.Config, keyPath string) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if resolved, errResolve := util.ResolveAuthDir(cfg.AuthDir); errResolve == nil {
		cfg.AuthDir = resolved
	}
	rawPath := strings.TrimSpace(keyPath)
	if rawPath == "" {
		log.Errorf("vertex-import: missing service account key path")
		return
	}
	data, errRead := os.ReadFile(rawPath)
	if errRead != nil {
		log.Errorf("vertex-import: read file failed: %v", errRead)
		return
	}
	var sa map[string]any
	if errUnmarshal := json.Unmarshal(data, &sa); errUnmarshal != nil {
		log.Errorf("vertex-import: invalid service account json: %v", errUnmarshal)
		return
	}
	// Validate and normalize private_key before saving
	normalizedSA, errFix := vertex.NormalizeServiceAccountMap(sa)
	if errFix != nil {
		log.Errorf("vertex-import: %v", errFix)
		return
	}
	sa = normalizedSA
	email, _ := sa["client_email"].(string)
	projectID, _ := sa["project_id"].(string)
	if strings.TrimSpace(projectID) == "" {
		log.Errorf("vertex-import: project_id missing in service account json")
		return
	}
	if strings.TrimSpace(email) == "" {
		// Keep empty email but warn
		log.Warn("vertex-import: client_email missing in service account json")
	}
	// Default location if not provided by user. Can be edited in the saved file later.
	location := "us-central1"

	fileName := fmt.Sprintf("vertex-%s.json", sanitizeFilePart(projectID))
	// Build auth record
	storage := &vertex.VertexCredentialStorage{
		ServiceAccount: sa,
		ProjectID:      projectID,
		Email:          email,
		Location:       location,
	}
	metadata := map[string]any{
		"service_account": sa,
		"project_id":      projectID,
		"email":           email,
		"location":        location,
		"type":            "vertex",
		"label":           labelForVertex(projectID, email),
	}
	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "vertex",
		FileName: fileName,
		Storage:  storage,
		Metadata: metadata,
	}

	store := sdkAuth.GetTokenStore()
	if setter, ok := store.(interface{ SetBaseDir(string) }); ok {
		setter.SetBaseDir(cfg.AuthDir)
	}
	path, errSave := store.Save(context.Background(), record)
	if errSave != nil {
		log.Errorf("vertex-import: save credential failed: %v", errSave)
		return
	}
	fmt.Printf("Vertex credentials imported: %s\n", path)
}

func sanitizeFilePart(s string) string {
	out := strings.TrimSpace(s)
	replacers := []string{"/", "_", "\\", "_", ":", "_", " ", "-"}
	for i := 0; i < len(replacers); i += 2 {
		out = strings.ReplaceAll(out, replacers[i], replacers[i+1])
	}
	return out
}

func labelForVertex(projectID, email string) string {
	p := strings.TrimSpace(projectID)
	e := strings.TrimSpace(email)
	if p != "" && e != "" {
		return fmt.Sprintf("%s (%s)", p, e)
	}
	if p != "" {
		return p
	}
	if e != "" {
		return e
	}
	return "vertex"
}
